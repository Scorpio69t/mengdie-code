// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/memory/proposal"
	"github.com/Scorpio69t/mengdie-code/internal/session"
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

// openTestProposalStoreWithSession mirrors openTestProposalStore but also
// hands back the underlying *session.SQLiteStore so the revert test can
// seed a synthetic proposal_applies row via raw SQL. The Store layer
// does not yet expose a public "insert audit row" surface (Task 1 kept
// it private behind Store.Apply + an executor), so a direct INSERT via
// sessionStore.DB() is the cheapest way to put the audit table in a
// known state for the CLI dispatcher. The session store is closed via
// t.Cleanup so the test owns its lifecycle cleanly.
func openTestProposalStoreWithSession(t *testing.T, state *appTestState) (*proposal.Store, *session.SQLiteStore) {
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
	return proposal.Open(sessionStore.DB(), state.app.now), sessionStore
}

// openTestApplyStores returns a memory.Store + proposal.Store pair bound to
// the same session store so the apply CLI test can seed a memory row and
// the proposal that targets it from the same connection. Mirrors
// openTestProposalStore but adds memory.OpenMemory on top so the
// DefaultApplyExecutor's memStore.UpgradeMemory path can resolve the
// target id at apply time. t.Cleanup closes the underlying session store
// exactly once even though two wrappers share it.
func openTestApplyStores(t *testing.T, state *appTestState) (*memory.Store, *proposal.Store) {
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
	return memory.OpenMemory(sessionStore), proposal.Open(sessionStore.DB(), state.app.now)
}

// reflectApplyTestTime is a fixed clock shared by the apply tests so the
// proposal_applies.applied_at stamp is deterministic. proposalTestTime
// lives in the proposal package as an unexported symbol, so this test
// package (package app) cannot import it — local copy keeps the
// dependency footprint to zero.
var reflectApplyTestTime = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

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

// TestReflectApplyApprovedProposal covers the v0.2 apply happy path:
// an approved memory_upgrade proposal targets a seeded inferred memory,
// the executor promotes it to explicit, and the CLI prints the literal
// "result=success" line + exits 0. The proposal payload includes the
// memory id + new_claim + new_authority triple so ApplyMemoryUpgrade
// can route through memStore.UpgradeMemory without falling into the
// "missing payload" branch. The Output contains "applied <id>" so a
// scripted wrapper can grep the id without parsing JSON.
func TestReflectApplyApprovedProposal(t *testing.T) {
	state := setupAppTestState(t)
	memStore, propStore := openTestApplyStores(t, state)

	saved, err := memStore.Save(context.Background(), memory.Memory{
		Claim:     "原记忆：项目入口",
		Authority: memory.AuthorityInferred,
		Scope:     memory.Scope{Kind: "task", Value: "apply-happy-test"},
		Source:    memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: "test:1"},
	})
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	inserted, err := propStore.Insert(context.Background(), proposal.Proposal{
		Kind:  proposal.KindMemoryUpgrade,
		Title: "升级记忆到 explicit",
		Body: proposal.ProposalBody{
			Kind: "memory_upgrade",
			Payload: map[string]any{
				"memory_id":     saved.ID,
				"new_claim":     "升级后的记忆：项目入口",
				"new_authority": "explicit",
			},
		},
		Status:     proposal.StatusApproved,
		ObservedAt: reflectApplyTestTime,
	})
	if err != nil {
		t.Fatalf("Insert proposal: %v", err)
	}

	state.stdout.Reset()
	state.stderr.Reset()
	code := runApp(state, []string{"reflect", "apply", inserted.ID})
	if code != ExitOK {
		t.Fatalf("apply exit=%d stderr=%q stdout=%q", code, state.stderr.String(), state.stdout.String())
	}
	out := state.stdout.String()
	if !strings.Contains(out, "applied "+inserted.ID) {
		t.Fatalf("apply output missing applied id: %q", out)
	}
	if !strings.Contains(out, "result=success") {
		t.Fatalf("apply output missing result=success: %q", out)
	}
}

// TestReflectApplyRejectsNotApproved covers spec §5 exit 2 (invalid
// input): Store.Apply rejects a not-approved row with
// ErrProposalNotApplicable before invoking the executor. exitForStoreError
// must map that sentinel to ExitInvalidInput so a wrapper can
// distinguish "you forgot to approve this first" from a generic write
// failure (ExitRunError). The proposal's payload is intentionally
// empty — Apply's status guard fires before the executor ever sees it,
// so the missing-payload branch is unreachable on this code path.
func TestReflectApplyRejectsNotApproved(t *testing.T) {
	state := setupAppTestState(t)
	_, propStore := openTestApplyStores(t, state)

	inserted, err := propStore.Insert(context.Background(), proposal.Proposal{
		Kind:       proposal.KindMemoryUpgrade,
		Title:      "未审批的提案",
		Body:       proposal.ProposalBody{Kind: "memory_upgrade"},
		Status:     proposal.StatusProposed,
		ObservedAt: reflectApplyTestTime,
	})
	if err != nil {
		t.Fatalf("Insert proposal: %v", err)
	}

	state.stdout.Reset()
	state.stderr.Reset()
	code := runApp(state, []string{"reflect", "apply", inserted.ID})
	if code != ExitInvalidInput {
		t.Fatalf("apply want exit %d, got %d stderr=%q stdout=%q",
			ExitInvalidInput, code, state.stderr.String(), state.stdout.String())
	}
}

// TestReflectRevertAppliedProposal covers the v0.2 revert happy path:
// an approved memory_upgrade proposal has a matching proposal_applies
// audit row seeded via raw SQL (Store.Apply is private behind an
// executor; the test only needs the audit row's existence). The CLI
// must exit 0 and render the literal "reverted <id>" token so a
// scripted wrapper can grep for completion without parsing JSON.
func TestReflectRevertAppliedProposal(t *testing.T) {
	state := setupAppTestState(t)
	propStore, sessionStore := openTestProposalStoreWithSession(t, state)

	saved, err := propStore.Insert(context.Background(), proposal.Proposal{
		Kind:       proposal.KindMemoryUpgrade,
		Title:      "x",
		Body:       proposal.ProposalBody{Kind: "memory_upgrade"},
		Status:     proposal.StatusApproved,
		ObservedAt: reflectApplyTestTime,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, derr := sessionStore.DB().ExecContext(context.Background(),
		`INSERT INTO proposal_applies (id, proposal_id, kind, target, result, applied_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"apply_x", saved.ID, "memory_upgrade", "mem_x", "success",
		reflectApplyTestTime.UTC().Format(time.RFC3339Nano),
	); derr != nil {
		t.Fatalf("seed apply row: %v", derr)
	}

	state.stdout.Reset()
	state.stderr.Reset()
	code := runApp(state, []string{"reflect", "revert", saved.ID})
	if code != ExitOK {
		t.Fatalf("revert exit=%d stderr=%q stdout=%q",
			code, state.stderr.String(), state.stdout.String())
	}
	if !strings.Contains(state.stdout.String(), "reverted "+saved.ID) {
		t.Fatalf("revert output missing: %q", state.stdout.String())
	}
}

// TestReflectRevertFailsNotApplied covers spec §5 exit 3 (not-found):
// an approved proposal with no matching proposal_applies audit row
// must surface ErrProposalNotApplied via exitForStoreError → ExitNotFound.
// The proposal_id is well-formed (the proposal row exists), but the
// audit table is empty for it, so the CLI cannot mark anything reverted.
// Distinct from ErrProposalNotFound which fires when the proposal id
// itself is absent — here the id resolves but the apply-side precondition
// is unmet.
func TestReflectRevertFailsNotApplied(t *testing.T) {
	state := setupAppTestState(t)
	propStore, _ := openTestProposalStoreWithSession(t, state)

	saved, err := propStore.Insert(context.Background(), proposal.Proposal{
		Kind:       proposal.KindMemoryUpgrade,
		Title:      "x",
		Body:       proposal.ProposalBody{Kind: "memory_upgrade"},
		Status:     proposal.StatusApproved,
		ObservedAt: reflectApplyTestTime,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// no apply row inserted

	state.stdout.Reset()
	state.stderr.Reset()
	code := runApp(state, []string{"reflect", "revert", saved.ID})
	if code != ExitNotFound {
		t.Fatalf("revert want exit %d, got %d stderr=%q stdout=%q",
			ExitNotFound, code, state.stderr.String(), state.stdout.String())
	}
}
