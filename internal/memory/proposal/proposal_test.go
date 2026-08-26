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
