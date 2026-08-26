// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package app 的 reflect 子命令实现 spec §1.4 的 4 个 CLI：reflect /
// proposals / approve / reject。每个子命令共享同一份 `state.db`（同 session），
// exit 码严格对应 spec §5 的 0..6 编号。`apply` 子命令按 spec §1.4 显式延期
// 到 v0.2（arch §9.4 限制），所以本文件只搭好 4 个 review-only CLI。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	proposal "github.com/Scorpio69t/mengdie-code/internal/memory/proposal"
)

// dispatchReflect is the top-level sub-router invoked from App.Run for
// `mengdie reflect <sub> ...`. With no args (or when args[0] is a flag
// belonging to `reflect` itself, e.g. `--since=7d` / `--max-sessions=5`)
// it runs the bare Pipeline.Reflect (Stages 1-5); otherwise it routes
// to one of the four review / apply subcommands.
func dispatchReflect(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	// If the first arg is a flag (starts with "-"), it belongs to
	// `reflect`, not a subcommand — pass everything through to
	// runReflect so spec §4.1 (`reflect --since=7d`) reaches the
	// FlagSet instead of being misread as an unknown subcommand.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return runReflect(ctx, args, a, stdout, stderr)
	}
	switch args[0] {
	case "proposals":
		return runReflectProposals(ctx, args[1:], a, stdout, stderr)
	case "approve":
		return runReflectApprove(ctx, args[1:], a, stdout, stderr)
	case "reject":
		return runReflectReject(ctx, args[1:], a, stdout, stderr)
	case "apply":
		return runReflectApply(ctx, args[1:], a, stdout, stderr)
	default:
		if err := a.writeError("未知 reflect 子命令 %q\n", args[0]); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
}

// runReflect implements `mengdie reflect`. It parses --since /
// --max-sessions flags, runs the 5-stage Pipeline.Reflect, and prints a
// summary line so a scripted wrapper can grep "Generated N proposals"
// without parsing every id back out of the per-row lines below.
func runReflect(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	flags, common := a.newMemoryFlagSet("mengdie reflect", stderr)
	since := flags.String("since", "7d", "时间窗口 (e.g. 7d, 24h, 1h)")
	maxSessions := flags.Int("max-sessions", 5, "最大 session 数")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		if err := writeMemoryError(stderr, "reflect 不接受位置参数\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}

	sinceTime, err := parseSince(*since, a.now())
	if err != nil {
		if werr := writeMemoryError(stderr, "无效 since %q: %v\n", *since, err); werr != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}

	pipeline, sessionStore, _, code := a.openReflectPipeline(ctx, common)
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	proposals, err := pipeline.Reflect(ctx, proposal.ReflectOptions{
		Since:       sinceTime,
		MaxSessions: *maxSessions,
	})
	if err != nil {
		return exitForStoreError(err)
	}
	if _, err := fmt.Fprintf(stdout, "Generated %d proposals (since %s, %d sessions scanned):\n",
		len(proposals), *since, *maxSessions); err != nil {
		return ExitRunError
	}
	for _, p := range proposals {
		if _, err := fmt.Fprintf(stdout, "  %s  %s  %q (confidence %.2f)\n",
			p.ID, p.Kind, p.Title, p.Confidence); err != nil {
			return ExitRunError
		}
	}
	return ExitOK
}

// runReflectProposals implements `mengdie reflect proposals`. It accepts
// --status / --kind / --limit / --json flags, calls Store.List, and
// renders either the spec §1.4 default ASCII table or one JSON object per
// line so downstream `jq` / `grep` pipelines can stream the queue
// without buffering. Exit code 3 on a missing id (spec §5) — not
// reachable here because List never errors on absent rows, but the
// shared exitForStoreError mapping keeps the family consistent.
func runReflectProposals(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	flags, common := a.newMemoryFlagSet("mengdie reflect proposals", stderr)
	status := flags.String("status", "", "按 status 过滤（proposed / approved / rejected）")
	kind := flags.String("kind", "", "按 kind 过滤")
	limit := flags.Int("limit", 0, "最大返回条数（默认 20，上限 200）")
	jsonOutput := flags.Bool("json", false, "输出 JSON Lines")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		if err := writeMemoryError(stderr, "reflect proposals 不接受位置参数\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}

	store, sessionStore, _, code := a.openProposalStore(ctx, common)
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	rows, err := store.List(ctx, proposal.ListQuery{
		Status: proposal.ProposalStatus(*status),
		Kind:   proposal.ProposalKind(*kind),
		Limit:  *limit,
	})
	if err != nil {
		if errors.Is(err, proposal.ErrInvalidQuery) {
			if werr := writeMemoryError(stderr, "查询参数无效：%v\n", err); werr != nil {
				return ExitRunError
			}
			return ExitInvalidInput
		}
		return exitForStoreError(err)
	}
	return writeReflectProposalsTable(stdout, rows, *jsonOutput)
}

// writeReflectProposalsTable renders the spec §1.4 default ASCII table.
// Each row is a single line so long titles stay on one screenful; the
// renderer truncates titles to 60 characters to match the Tier 1
// catalogue cap (writeMemoryListTable) so audit and review stay visually
// consistent. JSON mode emits one Proposal per line via encoding/json so
// `jq` / `grep` can stream without buffering.
func writeReflectProposalsTable(stdout io.Writer, rows []proposal.Proposal, jsonOutput bool) int {
	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		for _, row := range rows {
			if err := encoder.Encode(row); err != nil {
				return ExitRunError
			}
		}
		return ExitOK
	}
	const header = "id | kind | title | confidence | observed_at"
	if _, err := fmt.Fprintln(stdout, header); err != nil {
		return ExitRunError
	}
	for _, row := range rows {
		title := row.Title
		if len([]rune(title)) > 60 {
			title = string([]rune(title)[:60]) + "..."
		}
		if _, err := fmt.Fprintf(stdout, "%s | %s | %s | %.2f | %s\n",
			row.ID, row.Kind, title, row.Confidence,
			row.ObservedAt.UTC().Format(time.RFC3339),
		); err != nil {
			return ExitRunError
		}
	}
	return ExitOK
}

// runReflectApprove implements `mengdie reflect approve <id>`. The CLI
// takes a positional id; everything else (status transition, reviewer
// stamping) is the Store.UpdateStatus contract. The reviewer falls back
// to "mengdie" when $USER is unset so a CI invocation that runs without
// an interactive shell still records an audit trail.
func runReflectApprove(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		if err := writeMemoryError(stderr, "用法：mengdie reflect approve <id>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	id := args[0]

	store, sessionStore, _, code := a.openProposalStore(ctx, commonFlagsForPositional(a))
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	reviewer := reflectReviewer()
	if err := store.UpdateStatus(ctx, id, proposal.StatusApproved, reviewer); err != nil {
		return exitForStoreError(err)
	}
	if _, err := fmt.Fprintf(stdout, "approved %s\n", id); err != nil {
		return ExitRunError
	}
	return ExitOK
}

// runReflectReject implements `mengdie reflect reject <id>`. The mirror
// of runReflectApprove with StatusRejected; reviewer stamping is
// identical so the audit trail tracks who approved / rejected what.
func runReflectReject(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		if err := writeMemoryError(stderr, "用法：mengdie reflect reject <id>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	id := args[0]

	store, sessionStore, _, code := a.openProposalStore(ctx, commonFlagsForPositional(a))
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	reviewer := reflectReviewer()
	if err := store.UpdateStatus(ctx, id, proposal.StatusRejected, reviewer); err != nil {
		return exitForStoreError(err)
	}
	if _, err := fmt.Fprintf(stdout, "rejected %s\n", id); err != nil {
		return ExitRunError
	}
	return ExitOK
}

// runReflectApply implements `mengdie reflect apply <id>`. It opens the
// full reflect pipeline so the proposal + memory layers are available,
// builds a DefaultApplyExecutor (the production Task 4 surface), and
// calls Store.Apply to dispatch by Kind. v0.2 ships with a nil policy
// engine — the file-write paths consult the gate only when one is
// wired, and the runtime resolver currently hands the executor an
// empty projectRoot. The CLI's job here is plumbing: load the stores,
// hand the executor to Store.Apply, and render the resulting
// ApplyResult as a single line so a wrapper can grep
// "result=<success|failed|denied_by_policy>" without parsing JSON.
//
// Exit mapping:
//   - ExitOK          — Store.Apply returned ApplyResultSuccess.
//   - ExitInvalidInput — Store.Apply returned ErrProposalNotApplicable
//     (not-approved / unknown kind) or ErrProposalAlreadyApplied
//     (reserved per proposal.go doc).
//   - ExitNotFound    — Store.Apply returned ErrProposalNotFound
//     (id absent in reflection_proposals).
//   - ExitRunError    — ApplyResult.Result != success (executor
//     reported a side-effect failure — write conflict, missing
//     payload, etc.). The CLI still renders the row so the operator
//     can see what went wrong in the proposal_applies audit trail.
func runReflectApply(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		if err := writeMemoryError(stderr, "用法：mengdie reflect apply <id>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	id := args[0]

	// openReflectPipeline returns the pipeline + mem store separately;
	// we still need a *proposal.Store to hand to Store.Apply, and the
	// pipeline's proposalStore field is unexported. Open a sibling
	// proposal.Store on the same *sql.DB — both wrappers are stateless
	// around the connection so the second Open is a zero-cost no-op.
	_, sessionStore, memStore, code := a.openReflectPipeline(ctx, commonFlagsForPositional(a))
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	propStore := proposal.Open(sessionStore.DB(), a.now)
	executor := proposal.NewDefaultApplyExecutor(
		memStore, propStore,
		nil, a.projectRoot, a.now,
	)
	result, err := propStore.Apply(ctx, id, executor)
	if err != nil {
		return exitForStoreError(err)
	}

	if _, werr := fmt.Fprintf(stdout, "applied %s: kind=%s target=%s result=%s\n",
		result.ProposalID, result.Kind, result.Target, result.Result); werr != nil {
		return ExitRunError
	}
	if result.Error != "" {
		if _, werr := fmt.Fprintf(stderr, "  error: %s\n", result.Error); werr != nil {
			return ExitRunError
		}
	}
	if result.Result != proposal.ApplyResultSuccess {
		return ExitRunError
	}
	return ExitOK
}

// parseSince accepts a trailing "d" / "h" / "m" suffix and returns
// now - duration. Empty string is treated as "now" (i.e. no past
// sessions) so a CLI default of "7d" supplied via the FlagSet never
// reaches this branch; the unit test suite can still pass "" to verify
// the no-op fallback without a hard-coded default.
func parseSince(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return now, nil
	}
	var (
		n    int
		unit time.Duration
	)
	switch {
	case strings.HasSuffix(value, "d"):
		n, _ = strconv.Atoi(strings.TrimSuffix(value, "d"))
		unit = 24 * time.Hour
	case strings.HasSuffix(value, "h"):
		n, _ = strconv.Atoi(strings.TrimSuffix(value, "h"))
		unit = time.Hour
	case strings.HasSuffix(value, "m"):
		n, _ = strconv.Atoi(strings.TrimSuffix(value, "m"))
		unit = time.Minute
	default:
		return time.Time{}, fmt.Errorf("unsupported duration %q (use Nd / Nh / Nm)", value)
	}
	if n <= 0 {
		return time.Time{}, fmt.Errorf("duration must be positive: %q", value)
	}
	return now.Add(-time.Duration(n) * unit), nil
}

// reflectReviewer returns the local OS user for the reviewer's audit
// stamp. Falls back to "mengdie" when $USER is unset so a CI invocation
// (which usually has no $USER) still records a non-empty reviewer.
func reflectReviewer() string {
	if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
		return user
	}
	return "mengdie"
}

// commonFlagsForPositional returns the empty commonFlags used by the
// approve / reject subcommands. They take no flags so a hand-rolled
// zero value is the right input — loadConfig reads from the App's
// environment-driven defaults (userConfigDir, lookupEnv) so no
// per-call configuration is needed.
func commonFlagsForPositional(a *App) *commonFlags {
	return &commonFlags{}
}
