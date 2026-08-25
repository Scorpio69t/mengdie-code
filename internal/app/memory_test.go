// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// setupAppTestState wires a real App against the per-test temp data directory
// the existing newTestApp helper already allocates, so `mengdie memory` has a
// `state.db` to write into. The returned state captures the buffered stdout /
// stderr so subcommand output assertions can read what was rendered.
func setupAppTestState(t *testing.T) *appTestState {
	t.Helper()
	application, stdout, stderr := newTestApp(t, nil)
	// openSessionServiceForLoaded resolves the data dir via Override if set,
	// else falls back to the resolved project dir. newTestApp already gives
	// us an isolated temp data dir; no further wiring is required.
	return &appTestState{app: application, stdout: stdout, stderr: stderr}
}

// runApp invokes the App dispatcher against the test's buffered stdout /
// stderr so each subcommand's output is captured for assertions.
func runApp(state *appTestState, args []string) int {
	return state.app.Run(context.Background(), args, false)
}

// appTestState bundles an App under test with the buffers its subcommands
// wrote to. The struct also lets future tests share fixtures without each
// call site re-constructing the App.
type appTestState struct {
	app    *App
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

// TestMemoryRememberAndListRoundTrip exercises the spec §5 happy path:
// `mengdie memory remember <claim> --scope project` writes a memory, and a
// follow-up `mengdie memory list --scope project` renders the saved claim in
// the default ASCII table output. Both calls must exit 0.
func TestMemoryRememberAndListRoundTrip(t *testing.T) {
	state := setupAppTestState(t)
	code := runApp(state, []string{"memory", "remember", "用 go test ./...", "--scope", "project"})
	if code != ExitOK {
		t.Fatalf("remember exit=%d stderr=%q", code, state.stderr.String())
	}
	code = runApp(state, []string{"memory", "list", "--scope", "project"})
	if code != ExitOK {
		t.Fatalf("list exit=%d stderr=%q", code, state.stderr.String())
	}
	if !strings.Contains(state.stdout.String(), "用 go test ./...") {
		t.Fatalf("list must show remembered claim: %q", state.stdout.String())
	}
}

// TestMemoryRejectsUnknownAuthority covers the spec §5 exit-code-2 contract:
// unknown `--authority` values must be rejected at parse time, before any DB
// connection is opened.
func TestMemoryRejectsUnknownAuthority(t *testing.T) {
	state := setupAppTestState(t)
	code := runApp(state, []string{"memory", "list", "--authority", "bogus"})
	if code != 2 {
		t.Fatalf("bogus authority must exit 2, got %d stderr=%q", code, state.stderr.String())
	}
}

// TestMemoryForgetMissingID covers the spec §5 exit-code-3 contract: asking
// `mengdie memory forget` to remove an id that does not exist must surface
// ErrMemoryNotFound from the Store as exit code 3.
func TestMemoryForgetMissingID(t *testing.T) {
	state := setupAppTestState(t)
	code := runApp(state, []string{"memory", "forget", "mem_does_not_exist"})
	if code != 3 {
		t.Fatalf("missing id must exit 3, got %d stderr=%q", code, state.stderr.String())
	}
}

// TestMemoryListStatusAutoApproved covers the M3 Slice 03 v0.1 simplification:
// `--status auto-approved` is a CLI-side alias that the CLI accepts at parse
// time and translates to the underlying SQLite CHECK constraint value
// `status=active`. The test seeds an explicit-memory row (which lands in
// status=active), then issues `mengdie memory list --status auto-approved`
// and asserts the rendered ASCII table contains the saved claim.
//
// Today this test is expected to fail because `auto-approved` is not yet in
// the allowed-statuses allow-list; the implementation that ships with Task 5
// (memory.go: memoryAllowedStatusAliases) makes it pass.
func TestMemoryListStatusAutoApproved(t *testing.T) {
	state := setupAppTestState(t)
	code := runApp(state, []string{"memory", "remember", "项目使用 edit_file 修改文件", "--scope", "project"})
	if code != ExitOK {
		t.Fatalf("remember exit=%d stderr=%q", code, state.stderr.String())
	}
	// Reset the buffered stdout so the assertion only reads what the list
	// command emitted, not the remember output that preceded it.
	state.stdout.Reset()

	code = runApp(state, []string{"memory", "list", "--status", "auto-approved"})
	if code != ExitOK {
		t.Fatalf("list --status auto-approved must exit 0, got %d stderr=%q", code, state.stderr.String())
	}
	if !strings.Contains(state.stdout.String(), "项目使用 edit_file 修改文件") {
		t.Fatalf("list --status auto-approved must surface the auto-promoted claim: %q", state.stdout.String())
	}
}

// TestMemoryWhyShowsAuthorityRankGap covers M3 Slice 04 Task 3: when a memory
// has cross-authority peers in its Conflicts section, the `memory why` output
// must surface the Authority rank gap so a human auditor can see at a glance
// which side the spec §4.2 row 3 dispute favours. The test seeds one explicit
// row and one inferred row in the same project scope; both rows land in
// status=disputed per the spec §4.2 row 3 enforcement (Task 2 commit 34e2411),
// so `list --status disputed` returns both and `why <id>` exposes the
// Conflicts section with a rank-1 (explicit) and rank-4 (inferred) peer. The
// assertion then requires the rank + gap lines to render with both integers
// so the auditor does not have to recalculate them.
func TestMemoryWhyShowsAuthorityRankGap(t *testing.T) {
	state := setupAppTestState(t)
	code := runApp(state, []string{"memory", "remember", "项目测试入口是 go test ./internal/memory/...", "--scope", "project"})
	if code != ExitOK {
		t.Fatalf("remember 1 exit=%d stderr=%q", code, state.stderr.String())
	}
	code = runApp(state, []string{"memory", "remember", "项目测试入口是 make test", "--scope", "project", "--authority", "inferred"})
	if code != ExitOK {
		t.Fatalf("remember 2 exit=%d stderr=%q", code, state.stderr.String())
	}

	// Grab the first disputed id from the JSON list — the assertion below
	// is `why`-driven, so the precise id does not matter; either side of the
	// dispute exposes a Conflicts section with the other side as peer.
	state.stdout.Reset()
	code = runApp(state, []string{"memory", "list", "--status", "disputed", "--json"})
	if code != ExitOK {
		t.Fatalf("list exit=%d stderr=%q", code, state.stderr.String())
	}
	var firstID string
	for _, line := range strings.Split(strings.TrimSpace(state.stdout.String()), "\n") {
		if line == "" {
			continue
		}
		var row struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &row); err == nil && row.ID != "" {
			firstID = row.ID
			break
		}
	}
	if firstID == "" {
		t.Fatal("no disputed memory found")
	}

	state.stdout.Reset()
	code = runApp(state, []string{"memory", "why", firstID})
	if code != ExitOK {
		t.Fatalf("why exit=%d stderr=%q", code, state.stderr.String())
	}
	out := state.stdout.String()
	if !strings.Contains(out, "authority_rank_gap") {
		t.Fatalf("why output missing authority_rank_gap: %q", out)
	}
	if !strings.Contains(out, "rank 1") || !strings.Contains(out, "rank 4") {
		t.Fatalf("why output missing rank numbers: %q", out)
	}
}
