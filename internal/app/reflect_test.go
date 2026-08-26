// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/memory/proposal"
)

// openTestProposalStore returns a proposal.Store backed by the test App's
// data dir so tests bypass the CLI to seed rows the CLI dispatcher will
// later read. The session store is closed via t.Cleanup so each test
// independently owns its own connection lifecycle — sharing one across tests
// would race the SQLite writer. Mirrors the production openProposalStore
// wiring (data dir → session.OpenSQLite → proposal.Open) so the inserted
// row lives in the same file the CLI reads.
func openTestProposalStore(t *testing.T, state *appTestState) *proposal.Store {
	t.Helper()
	loaded, err := state.app.loadConfig(&commonFlags{})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	sessionStore, _, code := state.app.openSessionServiceForLoaded(context.Background(), loaded)
	if code != ExitOK {
		t.Fatalf("openSessionServiceForLoaded code=%d", code)
	}
	t.Cleanup(func() { _ = sessionStore.Close() })
	return proposal.Open(sessionStore.DB(), state.app.now)
}

// seedReflectProposal inserts one proposed-row directly via the store and
// returns its durable id. Skips the Pipeline (Stages 1-5) entirely because
// the CLI tests are about CLI behaviour — running the Pipeline inside the
// CLI test would force every test to seed session events too, which would
// pull Pipeline dependencies into the CLI test file (against brief).
func seedReflectProposal(t *testing.T, state *appTestState) string {
	t.Helper()
	store := openTestProposalStore(t, state)
	inserted, err := store.Insert(context.Background(), proposal.Proposal{
		Kind:       proposal.KindMemoryUpgrade,
		Title:      "测试提案：用户偏好 edit_file",
		Body:       proposal.ProposalBody{Kind: "test", Payload: map[string]any{"ratio": 0.8}},
		Confidence: 0.5,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return inserted.ID
}

// TestReflectRunsAndStoresProposals covers `mengdie reflect` in a fresh
// state: no sessions, no memories → Pipeline.scan returns 0 sessions →
// 0 proposals. The CLI must exit 0 with "Generated 0 proposals" so the
// caller can tell the pipeline actually ran (vs. silently no-op'ing).
func TestReflectRunsAndStoresProposals(t *testing.T) {
	state := setupAppTestState(t)
	code := runApp(state, []string{"reflect"})
	if code != ExitOK {
		t.Fatalf("reflect exit=%d stderr=%q", code, state.stderr.String())
	}
	if !strings.Contains(state.stdout.String(), "Generated") {
		t.Fatalf("reflect output missing Generated: %q", state.stdout.String())
	}
}

// TestReflectProposalsList covers `mengdie reflect proposals`: the
// dispatcher must render the spec §1.4 ASCII table header (`id | kind |
// title | confidence`) so auditors can scan a queue without parsing JSON.
func TestReflectProposalsList(t *testing.T) {
	state := setupAppTestState(t)
	seedReflectProposal(t, state)
	state.stdout.Reset()
	code := runApp(state, []string{"reflect", "proposals"})
	if code != ExitOK {
		t.Fatalf("reflect proposals exit=%d stderr=%q", code, state.stderr.String())
	}
	out := state.stdout.String()
	for _, want := range []string{"id |", "kind", "title", "confidence"} {
		if !strings.Contains(out, want) {
			t.Fatalf("reflect proposals table missing %q: %q", want, out)
		}
	}
}

// TestReflectApproveChangesStatus covers `mengdie reflect approve <id>`:
// a freshly-seeded proposed row must transition to approved and the CLI
// must print the literal "approved" token so a scripted wrapper can grep
// for completion without parsing the id back out of a longer line.
func TestReflectApproveChangesStatus(t *testing.T) {
	state := setupAppTestState(t)
	id := seedReflectProposal(t, state)
	state.stdout.Reset()
	code := runApp(state, []string{"reflect", "approve", id})
	if code != ExitOK {
		t.Fatalf("approve exit=%d stderr=%q", code, state.stderr.String())
	}
	if !strings.Contains(state.stdout.String(), "approved") {
		t.Fatalf("approve output missing 'approved': %q", state.stdout.String())
	}
}

// TestReflectRejectChangesStatus covers `mengdie reflect reject <id>`:
// the reject branch shares the wiring approve does (same openProposalStore
// + UpdateStatus path), so the assertion focuses on the rendered token.
func TestReflectRejectChangesStatus(t *testing.T) {
	state := setupAppTestState(t)
	id := seedReflectProposal(t, state)
	state.stdout.Reset()
	code := runApp(state, []string{"reflect", "reject", id})
	if code != ExitOK {
		t.Fatalf("reject exit=%d stderr=%q", code, state.stderr.String())
	}
	if !strings.Contains(state.stdout.String(), "rejected") {
		t.Fatalf("reject output missing 'rejected': %q", state.stdout.String())
	}
}

// TestReflectWithSinceFlag covers spec §4.1: `mengdie reflect --since=7d`
// must route the flag to runReflect (not the subcommand dispatcher). The
// dispatcher used to treat any leading arg as a subcommand, so this flag
// fell into the unknown-subcommand branch. Now args[0] starting with "-"
// passes through to runReflect, so a fresh state with no sessions still
// exits 0 with the canonical "Generated 0 proposals" line.
func TestReflectWithSinceFlag(t *testing.T) {
	state := setupAppTestState(t)
	code := runApp(state, []string{"reflect", "--since=7d"})
	if code != ExitOK {
		t.Fatalf("reflect --since=7d exit=%d stderr=%q", code, state.stderr.String())
	}
}

// TestReflectWithInvalidSince covers the runReflect error path: a flag
// that survives the dispatcher must still be validated by parseSince,
// which returns "unsupported duration" for any non Nd/Nh/Nm value. The
// CLI maps that to ExitInvalidInput (spec §5 exit 2) so a wrapper can
// distinguish a bad window from a successful 0-proposal run.
func TestReflectWithInvalidSince(t *testing.T) {
	state := setupAppTestState(t)
	code := runApp(state, []string{"reflect", "--since=garbage"})
	if code != ExitInvalidInput {
		t.Fatalf("reflect --since=garbage want exit %d, got %d stderr=%q",
			ExitInvalidInput, code, state.stderr.String())
	}
}

// TestReflectApproveBogusID covers spec §5 exit 3 (not-found): the
// approve subcommand already routed correctly (positional id), so this
// test guards against a future dispatcher refactor that breaks the
// subcommand path while fixing the flag path. The dispatcher must
// route "approve" to runReflectApprove, which calls UpdateStatus and
// surfaces ErrProposalNotFound via exitForStoreError → ExitNotFound.
func TestReflectApproveBogusID(t *testing.T) {
	state := setupAppTestState(t)
	code := runApp(state, []string{"reflect", "approve", "prop_does_not_exist"})
	if code != ExitNotFound {
		t.Fatalf("reflect approve bogus want exit %d, got %d stderr=%q",
			ExitNotFound, code, state.stderr.String())
	}
}
