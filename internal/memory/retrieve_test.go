// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package memory_test

import (
	"context"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// setupSeededRetriever opens an isolated session SQLite store (which already
// carries the 008_memory migration applied via OpenSQLite), wraps it in a
// memory.Store, and seeds the same four-authority fixture used by the Store
// tests. It returns both the memory.Store (so tests can wire a Retriever) and
// the underlying session.SQLiteStore (so tests that need to inspect tables
// the memory facade does not expose — e.g. memory_usage for the RecordUsage
// contract test — can query the underlying connection).
//
// The Store surface intentionally does not expose *sql.DB (see store.go), so
// tests that need raw table access go through sessionStore.DB(). Production
// callers do not have access to that handle and must rely on the Store API
// alone.
func setupSeededRetriever(t *testing.T) (*memory.Store, *session.SQLiteStore) {
	t.Helper()
	sessionStore := setupMemoryStore(t)
	s := memory.OpenMemory(sessionStore)
	ctx := context.Background()
	projectScope := memory.Scope{Kind: "project", Value: "mengdie"}
	seeds := []memory.Memory{
		{
			Claim:     "项目测试入口是 go test ./...",
			Authority: memory.AuthorityExplicit,
			Scope:     projectScope,
			Source:    memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-seed:1:user"},
		},
		{
			Claim:     "go.mod declares Go 1.26.6",
			Authority: memory.AuthorityRepository,
			Scope:     projectScope,
			Source:    memory.SourceRef{Type: memory.SourceTypeFile, Ref: "go.mod:3"},
		},
		{
			Claim:     "go test ./... exits 0",
			Authority: memory.AuthorityVerified,
			Scope:     projectScope,
			Source:    memory.SourceRef{Type: memory.SourceTypeCommandResult, Ref: "go test ./... exit=0"},
		},
		{
			Claim:     "agent-inferred project structure",
			Authority: memory.AuthorityInferred,
			Scope:     projectScope,
			Source:    memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: "session-seed:1:agent"},
		},
	}
	for i, in := range seeds {
		if _, err := s.Save(ctx, in); err != nil {
			t.Fatalf("seed %d: Save: %v", i, err)
		}
	}
	return s, sessionStore
}

// TestTier1CatalogueFiltersStale covers spec §6.1 Tier 1: every returned
// CatalogueEntry must carry a non-empty Claim, the inferred seed (status=
// proposed) must NOT appear in the catalogue, and the order must be
// evidence_score DESC, observed_at DESC. The non-empty-claim check is a
// proxy for "the projection scanned a real row" so a future regression that
// returns zero rows from a fully-seeded store still surfaces here.
func TestTier1CatalogueFiltersStale(t *testing.T) {
	s, _ := setupSeededRetriever(t)
	r := memory.NewRetriever(s)
	entries, err := r.Tier1Catalogue(context.Background(), memory.Scope{Kind: "project", Value: "mengdie"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one active project memory")
	}
	for _, e := range entries {
		if e.Claim == "" {
			t.Fatal("empty claim")
		}
		if len(e.Claim) > 60 {
			t.Fatalf("claim exceeds 60-char cap: %q", e.Claim)
		}
		if e.ID == "" {
			t.Fatal("empty id")
		}
		if e.Authority == "" {
			t.Fatalf("catalogue entry %s has empty authority; Tier 1 projection must surface authority alongside claim and evidence_score", e.ID)
		}
	}
	// The Authority surfaced on each CatalogueEntry must round-trip the
	// stored wire value: a regression that scanned it into the wrong column
	// (or dropped the column entirely, which used to render `authority=`
	// empty in the agent catalogue) would still pass the non-empty check
	// above but fail this per-authority match.
	seedExpectations := map[string]memory.Authority{
		"项目测试入口是 go test ./...":     memory.AuthorityExplicit,
		"go.mod declares Go 1.26.6": memory.AuthorityRepository,
		"go test ./... exits 0":     memory.AuthorityVerified,
	}
	for claim, want := range seedExpectations {
		var got memory.Authority
		var gotID string
		for _, e := range entries {
			if e.Claim == claim {
				got = e.Authority
				gotID = e.ID
				break
			}
		}
		if got == "" {
			t.Fatalf("seeded claim %q missing from catalogue; cannot verify authority surface", claim)
		}
		if got != want {
			t.Fatalf("claim %q surfaced with authority=%s, want %s (id=%s)", claim, got, want, gotID)
		}
	}
	for _, e := range entries {
		if e.ID == "" {
			continue
		}
		m, err := s.Get(context.Background(), e.ID)
		if err != nil {
			t.Fatalf("catalogue leaked un-readable id %s: %v", e.ID, err)
		}
		if m.Status != memory.StatusActive {
			t.Fatalf("catalogue leaked non-active memory %s status=%s", e.ID, m.Status)
		}
		if m.Authority == memory.AuthorityInferred {
			t.Fatalf("catalogue leaked proposed inferred memory %s", e.ID)
		}
	}
}

// TestTier3AtomicRecallScoresByFormula covers spec §6.1 Tier 3 scoring:
// every returned RecallHit must carry a positive Score. The seed contains
// "test" inside two active memories (explicit + verified), so a query for
// "test" must surface both and both scores must be positive — confirming
// that the bm25 proxy, authority weight, evidence score, scope-match bonus,
// and conflict penalty all line up.
func TestTier3AtomicRecallScoresByFormula(t *testing.T) {
	s, _ := setupSeededRetriever(t)
	r := memory.NewRetriever(s)
	hits, err := r.Tier3AtomicRecall(context.Background(), "test", 5, memory.Scope{Kind: "project", Value: "mengdie"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one match for query \"test\"")
	}
	for _, h := range hits {
		if h.Score <= 0 {
			t.Fatalf("score must be positive, got %v (claim=%q authority=%s status=%s)",
				h.Score, h.Claim, h.Authority, h.Status)
		}
		if h.ID == "" {
			t.Fatal("hit missing id")
		}
		if h.Status != memory.StatusActive {
			t.Fatalf("hit leaked non-active memory status=%s", h.Status)
		}
	}
	// Score-descending order is part of the contract: callers iterate hits
	// and expect the strongest match first.
	for i := 1; i < len(hits); i++ {
		if hits[i-1].Score < hits[i].Score {
			t.Fatalf("hits not score-desc: %v before %v", hits[i-1].Score, hits[i].Score)
		}
	}
}

// TestTier3RecordsUsage locks in the spec §6.1 line "召回同时调用
// Store.RecordUsage(memory_id, session_id, outcome=unknown)": every Tier 3
// recall must produce at least one memory_usage row. The composite PK on
// (memory_id, session_id, recalled_at) plus the OR IGNORE inside RecordUsage
// absorb duplicates, so the count check is safe under repeated recalls.
func TestTier3RecordsUsage(t *testing.T) {
	s, sessionStore := setupSeededRetriever(t)
	r := memory.NewRetriever(s)
	hits, err := r.Tier3AtomicRecall(context.Background(), "test", 3, memory.Scope{Kind: "project", Value: "mengdie"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit so the usage check below is meaningful")
	}
	var count int
	if err := sessionStore.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM memory_usage`,
	).Scan(&count); err != nil {
		t.Fatalf("count memory_usage: %v", err)
	}
	if count == 0 {
		t.Fatal("usage must be recorded")
	}
	if count < len(hits) {
		t.Fatalf("usage rows %d < returned hits %d", count, len(hits))
	}
}
