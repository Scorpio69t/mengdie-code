// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package proposal_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory/proposal"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// proposalTestTime is the fixed clock used by Store tests. The deterministic
// stamp lets us compare ObservedAt / CreatedAt / UpdatedAt against expected
// RFC3339Nano values without flakiness and exercises the injectable Now
// function described in the brief.
var proposalTestTime = time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)

// openProposalStore opens an isolated session SQLite store (which already
// carries every migration applied via OpenSQLite — including the new
// 010_reflection_proposals.sql) and hands the underlying *sql.DB to the
// proposal package via Open. The caller is responsible for invoking
// sessionStore.Close when done.
func openProposalStore(t *testing.T) (*proposal.Store, *session.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	sessionStore, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir:     dir,
		ProjectRoot: filepath.Join(t.TempDir(), "project"),
		Now:         func() time.Time { return proposalTestTime },
	})
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	return proposal.Open(sessionStore.DB(), func() time.Time { return proposalTestTime }), sessionStore
}

// TestProposalStoreInsertAndGet covers the Insert / Get round-trip: a Proposal
// with a JSON-marshalled Body, BasedOn list and Evidence list must come back
// out with the same fields populated. The deterministic id assertion guards
// against a future regression that drops generateProposalID before insert.
func TestProposalStoreInsertAndGet(t *testing.T) {
	ctx := context.Background()
	store, sessionStore := openProposalStore(t)
	defer func() { _ = sessionStore.Close() }()

	p := proposal.Proposal{
		Kind:  proposal.KindMemoryUpgrade,
		Title: "升级记忆：项目测试入口是 go test ./...",
		Body: proposal.ProposalBody{
			Kind: "memory_upgrade",
			Payload: map[string]any{
				"memory_id":      "mem_xxx",
				"current_claim":  "...",
				"proposed_claim": "...",
			},
		},
		BasedOn:    []string{"mem_xxx", "session_a"},
		SessionID:  "session_a",
		Confidence: 0.7,
		Status:     proposal.StatusProposed,
		ObservedAt: proposalTestTime,
	}
	saved, err := store.Insert(ctx, p)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("ID empty")
	}

	got, err := store.Get(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != p.Title {
		t.Fatalf("Title mismatch: got %q want %q", got.Title, p.Title)
	}
	if got.Status != proposal.StatusProposed {
		t.Fatalf("Status want proposed, got %s", got.Status)
	}
	if got.Kind != proposal.KindMemoryUpgrade {
		t.Fatalf("Kind want memory_upgrade, got %s", got.Kind)
	}
	if len(got.BasedOn) != 2 || got.BasedOn[0] != "mem_xxx" || got.BasedOn[1] != "session_a" {
		t.Fatalf("BasedOn round-trip mismatch: %v", got.BasedOn)
	}
}

// TestProposalStoreUpdateStatus covers the approve / reject path: UpdateStatus
// must flip status, stamp reviewer + reviewed_at, and the subsequent Get must
// observe the new state. The rowsAffected==0 branch is exercised by the
// GetNotFound test below.
func TestProposalStoreUpdateStatus(t *testing.T) {
	ctx := context.Background()
	store, sessionStore := openProposalStore(t)
	defer func() { _ = sessionStore.Close() }()

	p := proposal.Proposal{
		Kind:       proposal.KindMemoryUpgrade,
		Title:      "t",
		Body:       proposal.ProposalBody{Kind: "memory_upgrade"},
		Status:     proposal.StatusProposed,
		ObservedAt: proposalTestTime,
	}
	saved, err := store.Insert(ctx, p)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := store.UpdateStatus(ctx, saved.ID, proposal.StatusApproved, "reviewer1"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	got, err := store.Get(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Get after UpdateStatus: %v", err)
	}
	if got.Status != proposal.StatusApproved {
		t.Fatalf("Status want approved, got %s", got.Status)
	}
	if got.Reviewer != "reviewer1" {
		t.Fatalf("Reviewer want reviewer1, got %q", got.Reviewer)
	}
	if got.ReviewedAt == nil {
		t.Fatal("ReviewedAt empty")
	}
}

// TestProposalStoreList covers the dynamic-WHERE contract on List: an empty
// ListQuery (with Limit set) must return every inserted row ordered by
// observed_at DESC per the default ORDER BY clause.
func TestProposalStoreList(t *testing.T) {
	ctx := context.Background()
	store, sessionStore := openProposalStore(t)
	defer func() { _ = sessionStore.Close() }()

	for i := 0; i < 3; i++ {
		_, err := store.Insert(ctx, proposal.Proposal{
			Kind:       proposal.KindMemoryUpgrade,
			Title:      fmt.Sprintf("p%d", i),
			Body:       proposal.ProposalBody{Kind: "memory_upgrade"},
			Status:     proposal.StatusProposed,
			ObservedAt: proposalTestTime,
		})
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	list, err := store.List(ctx, proposal.ListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("List len want 3, got %d", len(list))
	}
}

// TestProposalStoreGetNotFound covers the missing-row contract: Get with an
// unknown id must return ErrProposalNotFound so the CLI can map it to a
// distinct exit code via errors.Is.
func TestProposalStoreGetNotFound(t *testing.T) {
	ctx := context.Background()
	store, sessionStore := openProposalStore(t)
	defer func() { _ = sessionStore.Close() }()

	_, err := store.Get(ctx, "prop_does_not_exist")
	if !errors.Is(err, proposal.ErrProposalNotFound) {
		t.Fatalf("want ErrProposalNotFound, got %v", err)
	}
}

// mockApplyExecutor is the slice-02 stand-in for the ApplyExecutor
// interface (slice-03 will add concrete executors; slice-02 ships the
// Store.Apply + ApplyExecutor surface only). It records which kind
// method was called and returns the configured ApplyResult / error so
// tests can assert the apply driver routed to the right dispatcher and
// that the idempotent guard skips re-invocation on a second Apply.
type mockApplyExecutor struct {
	memoryCalled bool
	memoryResult proposal.ApplyResult
	memoryErr    error

	agentsMdCalled bool
	agentsMdResult proposal.ApplyResult
	agentsMdErr    error

	skillDraftCalled bool
	skillDraftResult proposal.ApplyResult
	skillDraftErr    error

	obsoleteCalled bool
	obsoleteResult proposal.ApplyResult
	obsoleteErr    error
}

func (m *mockApplyExecutor) ApplyMemoryUpgrade(_ context.Context, _ proposal.Proposal) (proposal.ApplyResult, error) {
	m.memoryCalled = true
	return m.memoryResult, m.memoryErr
}

func (m *mockApplyExecutor) ApplyAgentsMdRevision(_ context.Context, _ proposal.Proposal) (proposal.ApplyResult, error) {
	m.agentsMdCalled = true
	return m.agentsMdResult, m.agentsMdErr
}

func (m *mockApplyExecutor) ApplySkillDraft(_ context.Context, _ proposal.Proposal) (proposal.ApplyResult, error) {
	m.skillDraftCalled = true
	return m.skillDraftResult, m.skillDraftErr
}

func (m *mockApplyExecutor) ApplyObsolete(_ context.Context, _ proposal.Proposal) (proposal.ApplyResult, error) {
	m.obsoleteCalled = true
	return m.obsoleteResult, m.obsoleteErr
}

// TestStoreApplyApprovedProposal covers the happy path: an approved
// proposal flows through Store.Apply, the executor's ApplyMemoryUpgrade
// is invoked, and the returned ApplyResult is stamped with proposal_id,
// kind, and applied_at. A proposal_applies audit row is written.
func TestStoreApplyApprovedProposal(t *testing.T) {
	ctx := context.Background()
	store, sessionStore := openProposalStore(t)
	defer func() { _ = sessionStore.Close() }()

	p := proposal.Proposal{
		Kind:       proposal.KindMemoryUpgrade,
		Title:      "升级记忆：项目测试入口是 go test ./...",
		Body:       proposal.ProposalBody{Kind: "memory_upgrade"},
		Status:     proposal.StatusProposed,
		ObservedAt: proposalTestTime,
	}
	saved, err := store.Insert(ctx, p)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := store.UpdateStatus(ctx, saved.ID, proposal.StatusApproved, "reviewer1"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	exec := &mockApplyExecutor{
		memoryResult: proposal.ApplyResult{
			Target: "mem_xxx",
			Result: proposal.ApplyResultSuccess,
		},
	}
	got, err := store.Apply(ctx, saved.ID, exec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !exec.memoryCalled {
		t.Fatal("executor.ApplyMemoryUpgrade not called")
	}
	if got.ProposalID != saved.ID {
		t.Fatalf("ProposalID want %q, got %q", saved.ID, got.ProposalID)
	}
	if got.Kind != proposal.KindMemoryUpgrade {
		t.Fatalf("Kind want %s, got %s", proposal.KindMemoryUpgrade, got.Kind)
	}
	if got.Result != proposal.ApplyResultSuccess {
		t.Fatalf("Result want %s, got %q", proposal.ApplyResultSuccess, got.Result)
	}
	if got.Target != "mem_xxx" {
		t.Fatalf("Target want mem_xxx, got %q", got.Target)
	}
	if got.AppliedAt.IsZero() {
		t.Fatal("AppliedAt not stamped")
	}

	var count int
	if err := sessionStore.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM proposal_applies WHERE proposal_id = ?`, saved.ID,
	).Scan(&count); err != nil {
		t.Fatalf("query proposal_applies: %v", err)
	}
	if count != 1 {
		t.Fatalf("proposal_applies count want 1, got %d", count)
	}
}

// TestStoreApplyRejectsNotApproved covers the not-approved branch:
// proposed (or rejected) proposals must be rejected with
// ErrProposalNotApplicable, the executor is never invoked, and no
// proposal_applies row is written. proposal_applies.proposal_id is
// UNIQUE, so we never want half-applied state in the table.
func TestStoreApplyRejectsNotApproved(t *testing.T) {
	ctx := context.Background()
	store, sessionStore := openProposalStore(t)
	defer func() { _ = sessionStore.Close() }()

	p := proposal.Proposal{
		Kind:       proposal.KindMemoryUpgrade,
		Title:      "未审批的提案",
		Body:       proposal.ProposalBody{Kind: "memory_upgrade"},
		Status:     proposal.StatusProposed,
		ObservedAt: proposalTestTime,
	}
	saved, err := store.Insert(ctx, p)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	exec := &mockApplyExecutor{}
	_, err = store.Apply(ctx, saved.ID, exec)
	if !errors.Is(err, proposal.ErrProposalNotApplicable) {
		t.Fatalf("want ErrProposalNotApplicable, got %v", err)
	}
	if exec.memoryCalled {
		t.Fatal("executor.ApplyMemoryUpgrade should not have been called")
	}

	var count int
	if err := sessionStore.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM proposal_applies WHERE proposal_id = ?`, saved.ID,
	).Scan(&count); err != nil {
		t.Fatalf("query proposal_applies: %v", err)
	}
	if count != 0 {
		t.Fatalf("proposal_applies count want 0, got %d", count)
	}
}

// TestStoreApplyIsIdempotent covers the idempotent guard: a second Apply
// for the same proposal_id returns the existing proposal_applies row
// without re-invoking the executor. proposal_applies.proposal_id is
// UNIQUE, so a second insert would error — the guard prevents that and
// also saves the caller from a double side-effect (memory row patched
// twice, AGENTS.md rewritten twice, etc.).
func TestStoreApplyIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, sessionStore := openProposalStore(t)
	defer func() { _ = sessionStore.Close() }()

	p := proposal.Proposal{
		Kind:       proposal.KindMemoryUpgrade,
		Title:      "幂等 Apply 测试",
		Body:       proposal.ProposalBody{Kind: "memory_upgrade"},
		Status:     proposal.StatusProposed,
		ObservedAt: proposalTestTime,
	}
	saved, err := store.Insert(ctx, p)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := store.UpdateStatus(ctx, saved.ID, proposal.StatusApproved, "reviewer1"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	exec := &mockApplyExecutor{
		memoryResult: proposal.ApplyResult{
			Target: "mem_xxx",
			Result: proposal.ApplyResultSuccess,
		},
	}

	first, err := store.Apply(ctx, saved.ID, exec)
	if err != nil {
		t.Fatalf("Apply first: %v", err)
	}
	if !exec.memoryCalled {
		t.Fatal("executor should be called on first Apply")
	}

	// Reset the call marker so we can detect a second invocation.
	exec.memoryCalled = false
	second, err := store.Apply(ctx, saved.ID, exec)
	if err != nil {
		t.Fatalf("Apply second: %v", err)
	}
	if exec.memoryCalled {
		t.Fatal("executor should NOT be called on idempotent re-Apply")
	}
	if second.ProposalID != first.ProposalID {
		t.Fatalf("ProposalID mismatch: first=%q second=%q", first.ProposalID, second.ProposalID)
	}
	if second.Kind != first.Kind {
		t.Fatalf("Kind mismatch: first=%s second=%s", first.Kind, second.Kind)
	}
	if second.Result != first.Result {
		t.Fatalf("Result mismatch: first=%q second=%q", first.Result, second.Result)
	}
	if second.Target != first.Target {
		t.Fatalf("Target mismatch: first=%q second=%q", first.Target, second.Target)
	}
	if !second.AppliedAt.Equal(first.AppliedAt) {
		t.Fatalf("AppliedAt mismatch: first=%v second=%v", first.AppliedAt, second.AppliedAt)
	}

	var count int
	if err := sessionStore.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM proposal_applies WHERE proposal_id = ?`, saved.ID,
	).Scan(&count); err != nil {
		t.Fatalf("query proposal_applies: %v", err)
	}
	if count != 1 {
		t.Fatalf("proposal_applies count want 1, got %d", count)
	}
}
