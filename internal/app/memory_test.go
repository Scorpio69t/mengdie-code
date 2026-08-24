// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
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
