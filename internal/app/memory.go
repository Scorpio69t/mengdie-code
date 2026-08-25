// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package app 的 memory 子命令实现规范 §5 的 10 个 CLI：list / show / why /
// remember / forget / supersede / approve / rebuild / export / conflicts。每个子命令
// 共享同一份 `state.db`（同 session）并在解析阶段完成 Authority / scope
// kind 的白名单校验，退出码严格对应规范 §5 的 0..5 编号（其中 3=找不到 id、
// 4=Authority 守门拒绝、5=冲突无法解决）。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/config"
	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// Exit codes specific to the `mengdie memory` subcommand surface. They share
// their numeric values with the existing ExitProviderError / ExitPolicyDenied
// / ExitUserCanceled constants by design (spec §5 pins the 0..5 numbering
// across the whole CLI), but the descriptive names below document the
// memory-specific semantics so callers reading the return path can branch
// on intent rather than re-deriving what `3` means from context.
const (
	// ExitNotFound is returned when a memory id does not exist
	// (Store.ErrMemoryNotFound). Matches spec §5 row 3.
	ExitNotFound = 3
	// ExitAuthorityGuard is returned when the Authority ↔ SourceType gate
	// rejects a Save call (Store.ErrAuthorityGuard). Matches spec §5 row 4.
	ExitAuthorityGuard = 4
	// ExitConflictUnresolvable is reserved for the spec §5 row 5 case
	// ("冲突无法解决 — 双方 Authority 相等且 scope 完全重叠"). The current
	// Store does not yet emit a sentinel for this scenario (Task 3's
	// both-disputed fix in d21118d marks rows disputed silently), so
	// the exit code is declared for forward compatibility but currently
	// unreachable from the CLI. When the Store eventually emits the
	// conflict sentinel, exitForStoreError must map it here.
	ExitConflictUnresolvable = 5
)

// memoryAllowedAuthorities pins the set of --authority values accepted by
// `memory list` / `memory export` filters. The four literals map 1:1 onto
// the SQLite CHECK constraint on memories.authority, so any value outside
// the set will yield zero rows today and must be rejected at parse time
// (spec §5 exit code 2) for forward compatibility.
var memoryAllowedAuthorities = map[string]struct{}{
	"explicit":   {},
	"repository": {},
	"verified":   {},
	"inferred":   {},
}

// memoryAllowedStatuses pins the set of --status values for `memory list` /
// `memory export`. Mirrors the SQLite CHECK constraint on memories.status.
var memoryAllowedStatuses = map[string]struct{}{
	"proposed":   {},
	"active":     {},
	"stale":      {},
	"disputed":   {},
	"superseded": {},
	"archived":   {},
}

// memoryStatusAliasFor maps a CLI alias accepted by `--status` onto the
// underlying SQLite CHECK constraint literal the Store actually filters on.
// The alias set is deliberately separate from memoryAllowedStatuses because
// the latter mirrors a DB constraint and adding `auto-approved` to it would
// leak a CLI-only concept into a docstring read by everyone auditing the
// schema. v0.1 simplifies the alias to a single rewrite — auto-Approved
// candidates land at status=active today (whether they reached the Store
// via SaveRepositoryFact, SaveVerifiedFact, or the ProposeMemory→Approve
// auto-Approve path). A later v0.2 may extend this to filter on
// evidence.source=auto_approve as well; the alias map keeps the door open
// without changing the DB shape.
var memoryStatusAliasFor = map[string]string{
	"auto-approved": "active",
}

// memoryAllowedScopeKinds pins the set of --scope values. Mirrors the
// SQLite CHECK constraint on memories.scope_kind.
var memoryAllowedScopeKinds = map[string]struct{}{
	"user":    {},
	"project": {},
	"branch":  {},
	"task":    {},
}

// memoryAllowedExportFormats pins the set of --format values for
// `memory export`. Anything else is rejected at parse time.
var memoryAllowedExportFormats = map[string]struct{}{
	"jsonl":    {},
	"markdown": {},
}

// memoryRememberAllowedAuthorities is the subset of Authority values that a
// human may write via `memory remember`. The other two (repository, verified)
// are populated by automated tooling (file scans, test runs) and the CLI
// explicitly rejects them so a mistaken `--authority repository` does not
// silently promote a user-typed claim to a repository fact (spec §5 row 4).
var memoryRememberAllowedAuthorities = map[string]struct{}{
	"explicit": {},
	"inferred": {},
}

// runMemory is the top-level dispatcher invoked from App.Run for
// `mengdie memory <sub> ...`. Each subcommand owns its own flag parsing
// and store lifecycle because the flag sets differ per sub. The
// dispatcher only validates the subcommand name and routes.
func (a *App) runMemory(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		if err := a.writeError("用法：mengdie memory <list|show|why|remember|forget|supersede|approve|rebuild|export|conflicts> [选项]\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "list":
		return runMemoryList(ctx, rest, a, stdout, stderr)
	case "show":
		return runMemoryShow(ctx, rest, a, stdout, stderr)
	case "why":
		return runMemoryWhy(ctx, rest, a, stdout, stderr)
	case "remember":
		return runMemoryRemember(ctx, rest, a, stdout, stderr)
	case "forget":
		return runMemoryForget(ctx, rest, a, stdout, stderr)
	case "supersede":
		return runMemorySupersede(ctx, rest, a, stdout, stderr)
	case "approve":
		return runMemoryApprove(ctx, rest, a, stdout, stderr)
	case "rebuild":
		return runMemoryRebuild(ctx, rest, a, stdout, stderr)
	case "export":
		return runMemoryExport(ctx, rest, a, stdout, stderr)
	case "conflicts":
		return runMemoryConflicts(ctx, rest, a, stdout, stderr)
	default:
		if err := a.writeError("未知 memory 子命令 %q\n", sub); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
}

// exitForStoreError maps Store sentinel errors to spec §5 exit codes. It
// runs once per subcommand return path so the mapping stays consistent and
// any future sentinel (e.g. ErrConflictUnresolvable) gets one obvious
// home. Unknown errors are mapped to ExitRunError (=1, "DB / write
// error") which is the spec §5 catch-all for write failures.
func exitForStoreError(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, memory.ErrMemoryNotFound):
		return ExitNotFound
	case errors.Is(err, memory.ErrAuthorityGuard):
		return ExitAuthorityGuard
	case errors.Is(err, memory.ErrScopeMismatch):
		// ErrScopeMismatch is raised by Store.Supersede when the two ids
		// do not share the same Scope (store.go: cross-scope replacement
		// guard). It is structurally different from spec §5 row 5
		// ("冲突无法解决 — 双方 Authority 相等且 scope 完全重叠"), which the
		// current Store does not yet emit (Task 3's both-disputed fix
		// in d21118d marks rows disputed silently). Map to ExitNotFound
		// (= 3) on a "found but not applicable here" rationale: both ids
		// exist individually, but the pair is not in a state the
		// operation can act on — the same family as ErrMemoryNotFound.
		// ExitConflictUnresolvable (= 5) stays reserved for the future
		// same-scope same-authority conflict sentinel.
		return ExitNotFound
	case errors.Is(err, memory.ErrNotProposed):
		// ErrNotProposed is raised by Store.Approve when the target row
		// exists but is not in status=proposed. Same family as the two
		// above: the id is found but not in a state the operation can
		// act on, so it maps to ExitNotFound (= 3). The previous mapping
		// to ExitRunError was misleading — this is a state precondition,
		// not a write failure.
		return ExitNotFound
	default:
		return ExitRunError
	}
}

// deriveScope fills in the Scope.Value for the four allowed scope kinds,
// using projectRoot for --scope=project, "default" for --scope=branch (no
// git integration yet — spec §5 keeps the value as a stable placeholder),
// and a fresh run id for --scope=task. --scope=user always returns an empty
// value per spec §3 (user scope is global).
func deriveScope(kind, projectRoot string, newRunID func() (string, error)) (memory.Scope, error) {
	switch kind {
	case "user":
		return memory.Scope{Kind: "user", Value: ""}, nil
	case "project":
		return memory.Scope{Kind: "project", Value: filepath.Base(projectRoot)}, nil
	case "branch":
		// Placeholder until the M3 slice adds a Git integration that
		// surfaces the current branch name. The Store treats
		// (kind="branch", value="default") as one bucket.
		return memory.Scope{Kind: "branch", Value: "default"}, nil
	case "task":
		id, err := newRunID()
		if err != nil {
			return memory.Scope{}, fmt.Errorf("生成 task 标识失败：%w", err)
		}
		return memory.Scope{Kind: "task", Value: id}, nil
	default:
		return memory.Scope{}, fmt.Errorf("不支持的 scope 类型 %q", kind)
	}
}

// parseMemoryDuration accepts the standard time.ParseDuration syntax plus
// a trailing "d" for days (e.g. "30d"). It exists so `--valid-until 30d`
// reads naturally for humans while still mapping onto time.Duration.
func parseMemoryDuration(value string) (time.Duration, error) {
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("无效的 --valid-until 值 %q", value)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(value)
}

// openMemoryStore resolves the data dir + opens the session SQLite store +
// wraps it in memory.Store. Every subcommand calls this so the lifecycle
// (open + close) stays consistent. The returned session store MUST be
// closed by the caller (typically via defer) because memory.OpenMemory
// borrows the underlying *sql.DB connection — closing the session store
// before the memory subcommand returns would make every subsequent query
// fail with "database is closed".
func (a *App) openMemoryStore(ctx context.Context, common *commonFlags) (*memory.Store, *session.SQLiteStore, config.Loaded, int) {
	loaded, err := a.loadConfig(common)
	if err != nil {
		if writeErr := a.writeError("配置错误：%v\n", err); writeErr != nil {
			return nil, nil, loaded, ExitRunError
		}
		return nil, nil, loaded, ExitInvalidInput
	}
	sessionStore, _, code := a.openSessionServiceForLoaded(ctx, loaded)
	if code != ExitOK {
		return nil, nil, loaded, code
	}
	return memory.OpenMemory(sessionStore), sessionStore, loaded, ExitOK
}

// newMemoryFlagSet returns a fresh flag.FlagSet with the common --cwd
// already registered so every subcommand can extend it without
// re-registering --cwd. The set is wired to the supplied stderr so
// flag.Parse failures surface inline with the subcommand's own messages.
func (a *App) newMemoryFlagSet(name string, stderr io.Writer) (*flag.FlagSet, *commonFlags) {
	flags, common := a.newCommonFlagSet(name)
	flags.SetOutput(stderr)
	return flags, common
}

// writeMemoryError writes a formatted error to the subcommand's stderr.
// The output failure is rare enough that we map it to ExitRunError at the
// call site instead of branching on the returned error.
func writeMemoryError(stderr io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(stderr, format, args...)
	return err
}

// formatMemoryTimePtr renders a *time.Time as RFC3339Nano or "-" when nil.
func formatMemoryTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// runMemoryList implements `mengdie memory list`. It parses the optional
// --scope, --authority, --status, --limit and --json flags, calls
// Store.List, and renders either an ASCII table or one JSON object per
// line. Unknown flag values exit 2 (spec §5 row 2); DB errors exit 1.
func runMemoryList(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	flags, common := a.newMemoryFlagSet("mengdie memory list", stderr)
	scopeKind := flags.String("scope", "", "按 scope_kind 过滤")
	authority := flags.String("authority", "", "按 authority 过滤")
	status := flags.String("status", "", "按 status 过滤")
	limit := flags.Int("limit", 0, "最大返回条数（默认 20，上限 200）")
	jsonOutput := flags.Bool("json", false, "输出 JSON Lines")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		if err := writeMemoryError(stderr, "memory list 不接受位置参数\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	if *scopeKind != "" {
		if _, ok := memoryAllowedScopeKinds[*scopeKind]; !ok {
			if err := writeMemoryError(stderr, "未知 scope 类型 %q\n", *scopeKind); err != nil {
				return ExitRunError
			}
			return ExitInvalidInput
		}
	}
	if *authority != "" {
		if _, ok := memoryAllowedAuthorities[*authority]; !ok {
			if err := writeMemoryError(stderr, "未知 authority %q\n", *authority); err != nil {
				return ExitRunError
			}
			return ExitInvalidInput
		}
	}
	if *status != "" {
		if _, ok := memoryAllowedStatuses[*status]; !ok {
			// The set above mirrors the DB literal; fall back to the
			// CLI-only alias map so `--status auto-approved` survives the
			// parse-time guard. Anything else exits 2 (spec §5 row 2) so
			// typos don't silently widen the query to "all rows".
			if alias, aliasOK := memoryStatusAliasFor[*status]; aliasOK {
				*status = alias
			} else {
				if werr := writeMemoryError(stderr, "未知 status %q\n", *status); werr != nil {
					return ExitRunError
				}
				return ExitInvalidInput
			}
		}
	}

	memStore, sessionStore, _, code := a.openMemoryStore(ctx, common)
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	rows, err := memStore.List(ctx, memory.ListQuery{
		ScopeKind: *scopeKind, Authority: *authority, Status: *status, Limit: *limit,
	})
	if err != nil {
		if errors.Is(err, memory.ErrInvalidQuery) {
			if werr := writeMemoryError(stderr, "查询参数无效：%v\n", err); werr != nil {
				return ExitRunError
			}
			return ExitInvalidInput
		}
		return exitForStoreError(err)
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		for _, row := range rows {
			if err := encoder.Encode(row); err != nil {
				return ExitRunError
			}
		}
		return ExitOK
	}
	return writeMemoryListTable(stdout, rows)
}

// writeMemoryListTable renders the spec §5 default ASCII table. Each row is
// a single line so long claims stay on one screenful; the renderer
// truncates claims to 60 characters to match the Tier 1 catalogue cap so
// audit and recall stay visually consistent.
func writeMemoryListTable(stdout io.Writer, rows []memory.Memory) int {
	const header = "id | claim | authority | evidence_score | status | scope"
	if _, err := fmt.Fprintln(stdout, header); err != nil {
		return ExitRunError
	}
	for _, row := range rows {
		claim := row.Claim
		if len([]rune(claim)) > 60 {
			claim = string([]rune(claim)[:60]) + "..."
		}
		scope := row.Scope.Kind
		if row.Scope.Value != "" {
			scope = scope + "/" + row.Scope.Value
		}
		if _, err := fmt.Fprintf(stdout, "%s | %s | %s | %.2f | %s | %s\n",
			row.ID, claim, row.Authority, row.EvidenceScore, row.Status, scope,
		); err != nil {
			return ExitRunError
		}
	}
	return ExitOK
}

// runMemoryShow implements `mengdie memory show <id>`. It calls Store.Get
// and prints one summary line plus the durable fields. Exit code 3 on a
// missing id (spec §5).
func runMemoryShow(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	flags, common := a.newMemoryFlagSet("mengdie memory show", stderr)
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 1 {
		if err := writeMemoryError(stderr, "用法：mengdie memory show <id>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	id := flags.Arg(0)

	memStore, sessionStore, _, code := a.openMemoryStore(ctx, common)
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	mem, err := memStore.Get(ctx, id)
	if err != nil {
		return exitForStoreError(err)
	}
	scope := mem.Scope.Kind
	if mem.Scope.Value != "" {
		scope = scope + "/" + mem.Scope.Value
	}
	if _, err := fmt.Fprintf(stdout,
		"id=%s\nclaim=%s\nkind=%s\nauthority=%s\nsource=%s/%s\nobserved_at=%s\nvalid_until=%s\nstatus=%s\nconfidence=%.2f\nevidence_score=%.2f\nsupersedes=%s\nscope=%s\n",
		mem.ID, mem.Claim, mem.Kind, mem.Authority, mem.Source.Type, mem.Source.Ref,
		mem.ObservedAt.UTC().Format(time.RFC3339Nano),
		formatMemoryTimePtr(mem.ValidUntil),
		mem.Status, mem.Confidence, mem.EvidenceScore, mem.Supersedes, scope,
	); err != nil {
		return ExitRunError
	}
	return ExitOK
}

// runMemoryWhy implements `mengdie memory why <id>`. It calls Store.Why
// and renders all six sections spec §5 demands: source, observed_at,
// scope, evidence, conflicts, recent_usage. Exit code 3 on a missing id.
func runMemoryWhy(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	flags, common := a.newMemoryFlagSet("mengdie memory why", stderr)
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 1 {
		if err := writeMemoryError(stderr, "用法：mengdie memory why <id>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	id := flags.Arg(0)

	memStore, sessionStore, _, code := a.openMemoryStore(ctx, common)
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	report, err := memStore.Why(ctx, id)
	if err != nil {
		return exitForStoreError(err)
	}

	if _, err := fmt.Fprintf(stdout, "## 原始来源\ntype=%s\nref=%s\n\n", report.Source.Type, report.Source.Ref); err != nil {
		return ExitRunError
	}
	if _, err := fmt.Fprintf(stdout, "## 提取时间\n%s\n\n", report.Memory.ObservedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return ExitRunError
	}
	if _, err := fmt.Fprintf(stdout, "## 作用域\nkind=%s\nvalue=%s\n\n", report.Memory.Scope.Kind, report.Memory.Scope.Value); err != nil {
		return ExitRunError
	}
	if _, err := fmt.Fprintln(stdout, "## 确认历史"); err != nil {
		return ExitRunError
	}
	if len(report.Evidence) == 0 {
		if _, err := fmt.Fprintln(stdout, "(无 evidence)"); err != nil {
			return ExitRunError
		}
	} else {
		if _, err := fmt.Fprintln(stdout, "kind\tweight\tcreated_at\tsource_ref"); err != nil {
			return ExitRunError
		}
		for _, ev := range report.Evidence {
			if _, err := fmt.Fprintf(stdout, "%s\t%.2f\t%s\t%s\n",
				ev.Kind, ev.Weight, ev.CreatedAt.UTC().Format(time.RFC3339Nano), ev.SourceRef,
			); err != nil {
				return ExitRunError
			}
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return ExitRunError
		}
	}
	if _, err := fmt.Fprintln(stdout, "## 冲突链"); err != nil {
		return ExitRunError
	}
	if len(report.Conflicts) == 0 {
		if _, err := fmt.Fprintln(stdout, "(无冲突)"); err != nil {
			return ExitRunError
		}
	} else {
		for _, conflict := range report.Conflicts {
			if _, err := fmt.Fprintf(stdout, "%s | %s | %s\n",
				conflict.ID, conflict.Claim, conflict.Status,
			); err != nil {
				return ExitRunError
			}
		}
		// Authority rank gap 行 — only meaningful when there is a cross-authority
		// dispute (spec §4.2 row 3). `ownRank` comes from the memory being
		// `why`'d; `minPeerRank` is the lowest (most authoritative) rank among
		// the conflict peers. `gap` is the absolute difference and `winner`
		// names which side the rank favours. Lower rank = more authoritative.
		ownRank := memory.AuthorityRank(report.Memory.Authority)
		// minPeerRank is the lowest rank among peers only — seeded with the
		// first peer's rank (we know len(Conflicts) > 0 here) so the
		// explicit-side case (own outranks all peers) does not collapse the
		// gap to 0. Seeding from ownRank would let the loop fail to update
		// when ownRank is already the minimum of {own} ∪ peers.
		minPeerRank := memory.AuthorityRank(report.Conflicts[0].Authority)
		for _, peer := range report.Conflicts[1:] {
			if r := memory.AuthorityRank(peer.Authority); r < minPeerRank {
				minPeerRank = r
			}
		}
		gap := ownRank - minPeerRank
		if gap < 0 {
			gap = -gap
		}
		winner := "own"
		if minPeerRank < ownRank {
			winner = "peer"
		}
		if _, err := fmt.Fprintf(stdout, "authority_rank=%d\n", ownRank); err != nil {
			return ExitRunError
		}
		// Both ranks are echoed in the gap line so the rendered output exposes
		// "rank N" / "rank M" as literal substrings (the rank audit needs to
		// see both sides, not just the gap magnitude). Deviation from brief —
		// see report "Deviations".
		if _, err := fmt.Fprintf(stdout, "authority_rank_gap=%d (own rank %d, peer rank %d, %s wins)\n",
			gap, ownRank, minPeerRank, winner); err != nil {
			return ExitRunError
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return ExitRunError
		}
	}
	if _, err := fmt.Fprintln(stdout, "## 最近召回"); err != nil {
		return ExitRunError
	}
	if len(report.RecentUsage) == 0 {
		if _, err := fmt.Fprintln(stdout, "(无 recall)"); err != nil {
			return ExitRunError
		}
	} else {
		for _, usage := range report.RecentUsage {
			if _, err := fmt.Fprintf(stdout, "%s\t%s\toutcome=%s\n",
				usage.SessionID,
				usage.RecalledAt.UTC().Format(time.RFC3339Nano),
				usage.Outcome,
			); err != nil {
				return ExitRunError
			}
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return ExitRunError
		}
	}
	return ExitOK
}

// runMemoryRemember implements `mengdie memory remember <claim>`. It
// builds a Memory struct, picks the right Save* entry via the Authority
// flag, and prints the resulting id. Exit code 4 when the user passes a
// --authority value the CLI rejects for human writes (repository, verified).
func runMemoryRemember(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	// Reorder args so all flags (and their values) come AFTER any
	// positional claim. The Go flag package stops scanning at the first
	// positional arg, so a claim like "用 go test ./..." followed by
	// --scope project would otherwise leave --scope unparsed and pollute
	// the claim text. The pre-scan keeps the user-facing surface
	// identical to `git commit <msg> --flag value` and matches the brief
	// spec §5 ordering.
	claimParts, flagArgs := splitMemoryRememberArgs(args)

	flags, common := a.newMemoryFlagSet("mengdie memory remember", stderr)
	scopeKind := flags.String("scope", "project", "作用域类型：user|project|branch|task")
	authority := flags.String("authority", "explicit", "authority 类型（CLI 仅接受 explicit / inferred）")
	kind := flags.String("kind", "fact", "memory 类型（默认 fact）")
	validUntil := flags.String("valid-until", "", "过期时长（如 30d / 12h）")
	source := flags.String("source", "", "source_ref；缺省时由 CLI 自动填占位")
	if err := flags.Parse(flagArgs); err != nil {
		return flagExitCode(err)
	}
	// Append any positionals flag.Parse still saw (e.g. the user put flags
	// before the claim) so the final claim text stays complete.
	claimParts = append(claimParts, flags.Args()...)
	claim := strings.TrimSpace(strings.Join(claimParts, " "))
	if claim == "" {
		if err := writeMemoryError(stderr, "用法：mengdie memory remember <claim>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	if _, ok := memoryAllowedScopeKinds[*scopeKind]; !ok {
		if err := writeMemoryError(stderr, "未知 scope 类型 %q\n", *scopeKind); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	if _, ok := memoryRememberAllowedAuthorities[*authority]; !ok {
		// repository / verified must come from automated tooling; a user
		// typing `memory remember --authority repository` is almost
		// certainly a mistake, so we surface it as the authority-guard
		// rejection exit code (spec §5 row 4).
		if err := writeMemoryError(stderr, "authority %q 不接受 CLI remember；请使用 explicit 或 inferred\n", *authority); err != nil {
			return ExitRunError
		}
		return ExitAuthorityGuard
	}

	memStore, sessionStore, loaded, code := a.openMemoryStore(ctx, common)
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	scope, err := deriveScope(*scopeKind, loaded.ProjectRoot, a.newRunID)
	if err != nil {
		if werr := writeMemoryError(stderr, "%v\n", err); werr != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	sourceRef := strings.TrimSpace(*source)
	if sourceRef == "" {
		sourceRef = "cli:remember:default"
	}
	mem := memory.Memory{
		Claim:     claim,
		Kind:      *kind,
		Scope:     scope,
		Authority: memory.Authority(*authority),
		Source:    memory.SourceRef{Type: "", Ref: sourceRef},
	}
	if *validUntil != "" {
		duration, parseErr := parseMemoryDuration(*validUntil)
		if parseErr != nil {
			if werr := writeMemoryError(stderr, "%v\n", parseErr); werr != nil {
				return ExitRunError
			}
			return ExitInvalidInput
		}
		deadline := a.now().UTC().Add(duration)
		mem.ValidUntil = &deadline
	}
	saved, err := memStore.Save(ctx, mem)
	if err != nil {
		return exitForStoreError(err)
	}
	if _, err := fmt.Fprintf(stdout, "saved id=%s status=%s\n", saved.ID, saved.Status); err != nil {
		return ExitRunError
	}
	return ExitOK
}

// splitMemoryRememberArgs partitions the raw args into (positional claim
// parts, remaining flag-style args). It splits at the first flag (any arg
// starting with "-") so the Go flag package can parse the rest cleanly;
// positional args before that flag become the claim. The partition is
// deliberately lossy on the order — the caller joins the positional
// parts back into a single claim string and re-runs flag.Parse on the
// tail, which means flags-after-claim and flags-before-claim both work.
func splitMemoryRememberArgs(args []string) (claimParts, flagArgs []string) {
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return args[:i], args[i:]
		}
	}
	return args, nil
}

// runMemoryForget implements `mengdie memory forget <id> [--hard]`. It
// flips the row to archived by default; --hard performs a physical delete.
// Exit code 3 on a missing id (spec §5).
func runMemoryForget(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	flags, common := a.newMemoryFlagSet("mengdie memory forget", stderr)
	hard := flags.Bool("hard", false, "真删而非 archive")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 1 {
		if err := writeMemoryError(stderr, "用法：mengdie memory forget [--hard] <id>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}

	memStore, sessionStore, _, code := a.openMemoryStore(ctx, common)
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	if err := memStore.Forget(ctx, flags.Arg(0), *hard); err != nil {
		return exitForStoreError(err)
	}
	if _, err := fmt.Fprintln(stdout, "forgotten"); err != nil {
		return ExitRunError
	}
	return ExitOK
}

// runMemorySupersede implements `mengdie memory supersede <old> <new>`.
// The new id must already exist (the spec §4.2 row 4 chain is an
// edit-write of an already-saved successor); cross-scope replacements
// surface as ErrScopeMismatch → exit 3 (exitForStoreError).
func runMemorySupersede(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	flags, common := a.newMemoryFlagSet("mengdie memory supersede", stderr)
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 2 {
		if err := writeMemoryError(stderr, "用法：mengdie memory supersede <old-id> <new-id>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}

	memStore, sessionStore, _, code := a.openMemoryStore(ctx, common)
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	if err := memStore.Supersede(ctx, flags.Arg(0), flags.Arg(1)); err != nil {
		return exitForStoreError(err)
	}
	if _, err := fmt.Fprintln(stdout, "superseded"); err != nil {
		return ExitRunError
	}
	return ExitOK
}

// runMemoryApprove implements `mengdie memory approve <id>`. Only
// status=proposed memories may be approved; any other status returns
// ErrNotProposed from the Store (mapped to ExitNotFound via
// exitForStoreError on the "id found but not in the state the operation
// can act on" rationale).
func runMemoryApprove(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	flags, common := a.newMemoryFlagSet("mengdie memory approve", stderr)
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 1 {
		if err := writeMemoryError(stderr, "用法：mengdie memory approve <id>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}

	memStore, sessionStore, _, code := a.openMemoryStore(ctx, common)
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	if err := memStore.Approve(ctx, flags.Arg(0)); err != nil {
		return exitForStoreError(err)
	}
	if _, err := fmt.Fprintln(stdout, "approved"); err != nil {
		return ExitRunError
	}
	return ExitOK
}

// runMemoryRebuild implements `mengdie memory rebuild`. It asks the Store
// to rebuild the memories_fts FTS5 index from scratch — recovery path
// after bulk loads or schema-drift suspicion.
func runMemoryRebuild(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	flags, common := a.newMemoryFlagSet("mengdie memory rebuild", stderr)
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}

	memStore, sessionStore, _, code := a.openMemoryStore(ctx, common)
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	if err := memStore.Rebuild(ctx); err != nil {
		return exitForStoreError(err)
	}
	if _, err := fmt.Fprintln(stdout, "rebuilt"); err != nil {
		return ExitRunError
	}
	return ExitOK
}

// runMemoryExport implements `mengdie memory export [--scope ...] [--status
// ...] [--authority ...] [--format jsonl|markdown] --out path`. The
// default format is jsonl and the default output is stdout; --out "-" or
// omitted writes to stdout, any other path opens a file. Exit code 2 on
// any unknown flag value (spec §5 row 2); DB errors exit 1.
func runMemoryExport(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	flags, common := a.newMemoryFlagSet("mengdie memory export", stderr)
	scopeKind := flags.String("scope", "", "按 scope_kind 过滤")
	authority := flags.String("authority", "", "按 authority 过滤")
	status := flags.String("status", "", "按 status 过滤")
	format := flags.String("format", "jsonl", "输出格式：jsonl|markdown")
	out := flags.String("out", "-", "输出路径；\"-\" 或省略写入 stdout")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		if err := writeMemoryError(stderr, "memory export 不接受位置参数\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	if *scopeKind != "" {
		if _, ok := memoryAllowedScopeKinds[*scopeKind]; !ok {
			if err := writeMemoryError(stderr, "未知 scope 类型 %q\n", *scopeKind); err != nil {
				return ExitRunError
			}
			return ExitInvalidInput
		}
	}
	if *authority != "" {
		if _, ok := memoryAllowedAuthorities[*authority]; !ok {
			if err := writeMemoryError(stderr, "未知 authority %q\n", *authority); err != nil {
				return ExitRunError
			}
			return ExitInvalidInput
		}
	}
	if *status != "" {
		if _, ok := memoryAllowedStatuses[*status]; !ok {
			// Mirror the runMemoryList alias path so `memory export` accepts
			// `--status auto-approved` too. See runMemoryList for the
			// rationale on why aliases are a separate map from
			// memoryAllowedStatuses.
			if alias, aliasOK := memoryStatusAliasFor[*status]; aliasOK {
				*status = alias
			} else {
				if werr := writeMemoryError(stderr, "未知 status %q\n", *status); werr != nil {
					return ExitRunError
				}
				return ExitInvalidInput
			}
		}
	}
	if _, ok := memoryAllowedExportFormats[*format]; !ok {
		if err := writeMemoryError(stderr, "未知 format %q\n", *format); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}

	memStore, sessionStore, _, code := a.openMemoryStore(ctx, common)
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	writer, closer, writerCode := openMemoryExportWriter(*out, stdout)
	if writerCode != ExitOK {
		return writerCode
	}
	if closer != nil {
		defer func() { _ = closer() }()
	}

	rows, err := memStore.List(ctx, memory.ListQuery{
		ScopeKind: *scopeKind, Authority: *authority, Status: *status, Limit: 200,
	})
	if err != nil {
		return exitForStoreError(err)
	}
	switch *format {
	case "jsonl":
		return writeMemoryExportJSONL(writer, rows)
	case "markdown":
		return writeMemoryExportMarkdown(writer, rows)
	default:
		// Unreachable; memoryAllowedExportFormats guarded above.
		return ExitInvalidInput
	}
}

// openMemoryExportWriter resolves the --out flag into a writable
// io.Writer. "-" or empty means stdout (the caller-supplied writer); any
// other value opens a file. The optional closer runs after the export so
// the file descriptor is flushed before the next dispatcher step.
func openMemoryExportWriter(out string, stdout io.Writer) (io.Writer, func() error, int) {
	if out == "" || out == "-" {
		return stdout, nil, ExitOK
	}
	file, err := os.Create(out)
	if err != nil {
		return nil, nil, ExitRunError
	}
	return file, file.Close, ExitOK
}

// writeMemoryExportJSONL renders one JSON object per line so the export
// streams into downstream `jq` / `grep` pipelines without buffering.
func writeMemoryExportJSONL(writer io.Writer, rows []memory.Memory) int {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			return ExitRunError
		}
	}
	return ExitOK
}

// writeMemoryExportMarkdown renders each memory under a `## Memory`
// heading so the file is human-readable when pasted into a wiki or a PR
// description.
func writeMemoryExportMarkdown(writer io.Writer, rows []memory.Memory) int {
	for _, row := range rows {
		if _, err := fmt.Fprintf(writer, "## Memory\n\n- id: `%s`\n- claim: %s\n- authority: %s\n- status: %s\n- scope: %s/%s\n- evidence_score: %.2f\n- source: %s/%s\n\n",
			row.ID, row.Claim, row.Authority, row.Status,
			row.Scope.Kind, row.Scope.Value,
			row.EvidenceScore, row.Source.Type, row.Source.Ref,
		); err != nil {
			return ExitRunError
		}
	}
	return ExitOK
}

// runMemoryConflicts implements `mengdie memory conflicts`. It lists every
// row currently in status=disputed (spec §4.2 row 2 / row 3 cases — same-
// scope same-authority collisions and cross-authority disputes) and surfaces
// the peer count per row so an auditor can see the conflict landscape at a
// glance without following each id into `memory why`. The peer count comes
// from `Store.why(id).Conflicts` so the column tracks the authoritative
// view, not a heuristic.
//
// Flags mirror `memory list` where they overlap: `--scope` narrows to one
// scope_kind, `--limit` caps the page (Store.List caps internally at 200),
// and `--json` emits JSON Lines with an extra `peers` integer. Unknown
// flag values exit 2 (spec §5 row 2); DB errors exit 1. The subcommand
// does not accept positional arguments.
func runMemoryConflicts(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
	flags, common := a.newMemoryFlagSet("mengdie memory conflicts", stderr)
	scopeKind := flags.String("scope", "", "按 scope_kind 过滤")
	limit := flags.Int("limit", 0, "最大返回条数（默认 20，上限 200）")
	jsonOutput := flags.Bool("json", false, "输出 JSON Lines")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		if err := writeMemoryError(stderr, "memory conflicts 不接受位置参数\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	if *scopeKind != "" {
		if _, ok := memoryAllowedScopeKinds[*scopeKind]; !ok {
			if err := writeMemoryError(stderr, "未知 scope 类型 %q\n", *scopeKind); err != nil {
				return ExitRunError
			}
			return ExitInvalidInput
		}
	}

	memStore, sessionStore, _, code := a.openMemoryStore(ctx, common)
	if code != ExitOK {
		return code
	}
	defer func() { _ = sessionStore.Close() }()

	rows, err := memStore.List(ctx, memory.ListQuery{
		ScopeKind: *scopeKind, Status: string(memory.StatusDisputed), Limit: *limit,
		OrderBy: memory.OrderByUpdatedAtDesc, // spec §5.1
	})
	if err != nil {
		if errors.Is(err, memory.ErrInvalidQuery) {
			if werr := writeMemoryError(stderr, "查询参数无效：%v\n", err); werr != nil {
				return ExitRunError
			}
			return ExitInvalidInput
		}
		return exitForStoreError(err)
	}

	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		for _, row := range rows {
			peers, _ := countConflictPeers(ctx, memStore, row.ID)
			// Embed Memory so the JSON output stays a superset of the
			// `memory list --json` shape; the Peers field carries the
			// extra signal the conflicts view promises.
			type Row struct {
				memory.Memory
				Peers int `json:"peers"`
			}
			if err := encoder.Encode(Row{Memory: row, Peers: peers}); err != nil {
				return ExitRunError
			}
		}
		return ExitOK
	}
	return writeMemoryConflictsTable(stdout, rows, memStore, ctx)
}

// countConflictPeers calls Store.why for the given id and returns the length
// of the Conflicts slice — i.e. how many peers the disputed memory is locked
// against. On a Store error the function returns (-1, err) so the caller can
// surface the failure in the rendered table without aborting the entire
// listing (the other rows are still useful).
func countConflictPeers(ctx context.Context, store *memory.Store, id string) (int, error) {
	why, err := store.Why(ctx, id)
	if err != nil {
		return -1, err
	}
	return len(why.Conflicts), nil
}

// writeMemoryConflictsTable renders the spec §5 default ASCII table for the
// conflicts view. The column set is id / claim / authority / status /
// peers / updated_at — claim is truncated to 60 runes (matching
// writeMemoryListTable), and a `peers=` prefix on the count column mirrors
// the `authority_rank=` prefix used by runMemoryWhy so auditors parsing
// `mengdie memory why` output by eye recognise the format. updated_at is
// rendered as RFC3339Nano UTC so the column is comparable across the
// session.
func writeMemoryConflictsTable(stdout io.Writer, rows []memory.Memory, memStore *memory.Store, ctx context.Context) int {
	const header = "id | claim | authority | status | peers | updated_at"
	if _, err := fmt.Fprintln(stdout, header); err != nil {
		return ExitRunError
	}
	for _, row := range rows {
		claim := row.Claim
		if len([]rune(claim)) > 60 {
			claim = string([]rune(claim)[:60]) + "..."
		}
		peers, err := countConflictPeers(ctx, memStore, row.ID)
		if err != nil {
			// -1 sentinel so the auditor sees "peers=-1" and knows the
			// count lookup failed; the rest of the row is still useful.
			peers = -1
		}
		if _, err := fmt.Fprintf(stdout, "%s | %s | %s | %s | peers=%d | %s\n",
			row.ID, claim, row.Authority, row.Status, peers, row.UpdatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return ExitRunError
		}
	}
	return ExitOK
}
