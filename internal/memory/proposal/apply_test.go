// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package proposal_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/memory/proposal"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// openMemoryAndProposalStore opens one session SQLite store (already carrying
// every migration applied via OpenSQLite — including 008_memory and
// 010_reflection_proposals) and returns a *memory.Store + *proposal.Store
// bound to the same *sql.DB. Mirrors openProposalStore but adds the memory
// Store side so DefaultApplyExecutor tests can seed memories and verify the
// post-apply state. The caller is responsible for invoking
// sessionStore.Close.
func openMemoryAndProposalStore(t *testing.T) (*memory.Store, *proposal.Store, *session.SQLiteStore) {
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
	memStore := memory.OpenMemory(sessionStore)
	propStore := proposal.Open(sessionStore.DB(), func() time.Time { return proposalTestTime })
	return memStore, propStore, sessionStore
}

// TestDefaultApplyExecutorMemoryUpgrade covers the memory_upgrade happy
// path: an inferred memory seeded into a fresh task scope is promoted to
// explicit via ApplyMemoryUpgrade with a new claim, and the post-state
// reflects both the new claim and the new authority. The target id is
// echoed in ApplyResult.Target so the CLI audit row can render it without
// re-querying.
func TestDefaultApplyExecutorMemoryUpgrade(t *testing.T) {
	ctx := context.Background()
	memStore, propStore, sessionStore := openMemoryAndProposalStore(t)
	defer func() { _ = sessionStore.Close() }()

	saved, err := memStore.Save(ctx, memory.Memory{
		Claim:     "old claim",
		Authority: memory.AuthorityInferred,
		Scope:     memory.Scope{Kind: "task", Value: "apply-memory-upgrade-test"},
		Source:    memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: "test:1"},
	})
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	p := proposal.Proposal{
		Kind:  proposal.KindMemoryUpgrade,
		Title: "upgrade",
		Body: proposal.ProposalBody{
			Kind: "memory_upgrade",
			Payload: map[string]any{
				"memory_id":     saved.ID,
				"new_claim":     "upgraded claim",
				"new_authority": "explicit",
			},
		},
		Status:     proposal.StatusApproved,
		ObservedAt: proposalTestTime,
	}

	executor := proposal.NewDefaultApplyExecutor(memStore, propStore, nil, "", func() time.Time { return proposalTestTime })
	result, err := executor.ApplyMemoryUpgrade(ctx, p)
	if err != nil {
		t.Fatalf("ApplyMemoryUpgrade: %v", err)
	}
	if result.Result != proposal.ApplyResultSuccess {
		t.Fatalf("want %s, got %s: %s", proposal.ApplyResultSuccess, result.Result, result.Error)
	}
	if result.Target != saved.ID {
		t.Fatalf("Target want %q, got %q", saved.ID, result.Target)
	}

	got, err := memStore.Get(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Get after upgrade: %v", err)
	}
	if got.Claim != "upgraded claim" {
		t.Fatalf("Claim not upgraded: got %q", got.Claim)
	}
	if got.Authority != memory.AuthorityExplicit {
		t.Fatalf("Authority not upgraded: got %s", got.Authority)
	}
}

// TestDefaultApplyExecutorObsolete covers the obsolete happy path: an
// explicit memory is soft-archived via ApplyObsolete, leaving the row in
// storage but flipping status to archived so the Trust Set / list
// filters drop it. The post-state assertion guards against a future
// regression that accidentally hard-deletes (hard=false is the soft
// archive path used by Trust Set cleanup).
func TestDefaultApplyExecutorObsolete(t *testing.T) {
	ctx := context.Background()
	memStore, propStore, sessionStore := openMemoryAndProposalStore(t)
	defer func() { _ = sessionStore.Close() }()

	saved, err := memStore.Save(ctx, memory.Memory{
		Claim:     "obsolete",
		Authority: memory.AuthorityExplicit,
		Scope:     memory.Scope{Kind: "task", Value: "apply-obsolete-test"},
		Source:    memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "test:1"},
	})
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	p := proposal.Proposal{
		Kind:  proposal.KindObsolete,
		Title: "obs",
		Body: proposal.ProposalBody{
			Kind:    "obsolete",
			Payload: map[string]any{"memory_id": saved.ID},
		},
		Status:     proposal.StatusApproved,
		ObservedAt: proposalTestTime,
	}

	executor := proposal.NewDefaultApplyExecutor(memStore, propStore, nil, "", func() time.Time { return proposalTestTime })
	result, err := executor.ApplyObsolete(ctx, p)
	if err != nil {
		t.Fatalf("ApplyObsolete: %v", err)
	}
	if result.Result != proposal.ApplyResultSuccess {
		t.Fatalf("want %s, got %s: %s", proposal.ApplyResultSuccess, result.Result, result.Error)
	}
	if result.Target != saved.ID {
		t.Fatalf("Target want %q, got %q", saved.ID, result.Target)
	}

	got, err := memStore.Get(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Get after obsolete: %v", err)
	}
	if got.Status != memory.StatusArchived {
		t.Fatalf("Status want archived, got %s", got.Status)
	}
}
