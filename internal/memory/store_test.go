// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package memory_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// memoryTestTime is the fixed clock used by Store tests. The deterministic
// stamp lets us compare UpdatedAt against expected RFC3339Nano values without
// flakiness and exercises the injectable Now function described in the brief.
var memoryTestTime = time.Date(2026, 8, 6, 10, 30, 0, 123, time.UTC)

// openMemoryStore opens an isolated session SQLite store (which already
// carries the 008_memory migration applied via OpenSQLite) and hands the
// underlying *sql.DB to the memory package via OpenMemory. The caller is
// responsible for invoking t.Cleanup to close the store.
func openMemoryStore(t *testing.T) *session.SQLiteStore {
	t.Helper()
	directory := t.TempDir()
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir: directory, ProjectRoot: filepath.Join(t.TempDir(), "project"),
		BusyTimeout: 250 * time.Millisecond,
		Now:         func() time.Time { return memoryTestTime },
	})
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	return store
}

func setupMemoryStore(t *testing.T) *session.SQLiteStore {
	t.Helper()
	store := openMemoryStore(t)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close memory store: %v", err)
		}
	})
	return store
}

// openSharedMemoryStore opens a second session SQLite store against the same
// DataDir as the first one. Two stores on one WAL file let the race tests
// exercise concurrent writers without sharing a single *sql.DB connection
// pool. The caller is responsible for closing both stores.
func openSharedMemoryStore(t *testing.T, directory string, busyTimeout time.Duration) *session.SQLiteStore {
	t.Helper()
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir: directory, ProjectRoot: filepath.Join(t.TempDir(), "project"),
		BusyTimeout: busyTimeout,
		Now:         func() time.Time { return memoryTestTime },
	})
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	return store
}

// TestSaveUserMemoryCreatesActive covers the Authority-explicit happy path:
// a memory with AuthorityExplicit + SourceTypeUserMessage must be persisted
// as StatusActive (not proposed) and a deterministic id must be assigned.
// The Authority assertion guards against a future regression that drops the
// routed value before insert.
func TestSaveUserMemoryCreatesActive(t *testing.T) {
	store := setupMemoryStore(t)
	s := memory.OpenMemory(store)
	in := memory.Memory{
		Claim: "项目用 go test ./...", Authority: memory.AuthorityExplicit,
		Scope:  memory.Scope{Kind: "project", Value: "mengdie"},
		Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-1:1:user"},
	}
	got, err := s.SaveUserMemory(context.Background(), in)
	if err != nil {
		t.Fatalf("SaveUserMemory: %v", err)
	}
	if got.Status != memory.StatusActive {
		t.Fatalf("status=%s, want active", got.Status)
	}
	if got.Authority != memory.AuthorityExplicit {
		t.Fatalf("authority=%s, want explicit", got.Authority)
	}
	if got.ID == "" {
		t.Fatal("id empty")
	}
}

// TestSaveRepositoryFactCreatesActive covers the Authority-repository path:
// SaveRepositoryFact must persist a memory with AuthorityRepository +
// SourceTypeFile and StatusActive. The Authority assertion guards against a
// future regression that drops the routed value before insert.
func TestSaveRepositoryFactCreatesActive(t *testing.T) {
	store := setupMemoryStore(t)
	s := memory.OpenMemory(store)
	in := memory.Memory{
		Claim:  "go.mod requires Go 1.26",
		Scope:  memory.Scope{Kind: "project", Value: "mengdie"},
		Source: memory.SourceRef{Type: memory.SourceTypeFile, Ref: "go.mod:3"},
	}
	got, err := s.SaveRepositoryFact(context.Background(), in)
	if err != nil {
		t.Fatalf("SaveRepositoryFact: %v", err)
	}
	if got.Status != memory.StatusActive {
		t.Fatalf("status=%s, want active", got.Status)
	}
	if got.Authority != memory.AuthorityRepository {
		t.Fatalf("authority=%s, want repository", got.Authority)
	}
	if got.Source.Type != memory.SourceTypeFile {
		t.Fatalf("source_type=%s, want file", got.Source.Type)
	}
}

// TestSaveVerifiedFactCreatesActive covers the Authority-verified path:
// SaveVerifiedFact must persist a memory with AuthorityVerified +
// SourceTypeCommandResult and StatusActive. The Authority assertion guards
// against a future regression that drops the routed value before insert.
func TestSaveVerifiedFactCreatesActive(t *testing.T) {
	store := setupMemoryStore(t)
	s := memory.OpenMemory(store)
	in := memory.Memory{
		Claim:  "go test ./... exits 0",
		Scope:  memory.Scope{Kind: "project", Value: "mengdie"},
		Source: memory.SourceRef{Type: memory.SourceTypeCommandResult, Ref: "go test ./... exit=0"},
	}
	got, err := s.SaveVerifiedFact(context.Background(), in)
	if err != nil {
		t.Fatalf("SaveVerifiedFact: %v", err)
	}
	if got.Status != memory.StatusActive {
		t.Fatalf("status=%s, want active", got.Status)
	}
	if got.Authority != memory.AuthorityVerified {
		t.Fatalf("authority=%s, want verified", got.Authority)
	}
	if got.Source.Type != memory.SourceTypeCommandResult {
		t.Fatalf("source_type=%s, want command_result", got.Source.Type)
	}
}

// TestProposeMemoryAlwaysProposed covers the inferred path: even when the
// caller does not explicitly request a proposed status, ProposeMemory must
// return StatusProposed (spec §4.1: inferred memories cannot become active
// without an explicit user approval step). The Authority assertion guards
// against a future regression that drops the routed value before insert.
func TestProposeMemoryAlwaysProposed(t *testing.T) {
	store := setupMemoryStore(t)
	s := memory.OpenMemory(store)
	in := memory.Memory{
		Claim: "推断出...", Authority: memory.AuthorityInferred,
		Scope:  memory.Scope{Kind: "project", Value: "mengdie"},
		Source: memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: "session-1:1:agent"},
	}
	got, err := s.ProposeMemory(context.Background(), in)
	if err != nil {
		t.Fatalf("ProposeMemory: %v", err)
	}
	if got.Status != memory.StatusProposed {
		t.Fatalf("inferred must be proposed, got %s", got.Status)
	}
	if got.Authority != memory.AuthorityInferred {
		t.Fatalf("authority=%s, want inferred", got.Authority)
	}
}

// TestSaveRejectsMismatchedAuthority exercises the spec §4.1 Authority↔
// SourceType guard at the Save* entry point: calling SaveUserMemory with a
// Source.Type that does not match the routed SourceTypeUserMessage must
// return ErrAuthorityGuard (errors.Is match).
func TestSaveRejectsMismatchedAuthority(t *testing.T) {
	store := setupMemoryStore(t)
	s := memory.OpenMemory(store)
	in := memory.Memory{
		Claim:  "should be rejected",
		Scope:  memory.Scope{Kind: "project", Value: "mengdie"},
		Source: memory.SourceRef{Type: memory.SourceTypeFile, Ref: "go.mod:3"},
	}
	_, err := s.SaveUserMemory(context.Background(), in)
	if !errors.Is(err, memory.ErrAuthorityGuard) {
		t.Fatalf("err=%v, want ErrAuthorityGuard", err)
	}
}

// TestSaveMarksBothDisputedOnSameScopeDifferentClaim locks in spec §4.2 row
// 2 ("双方都置 disputed"): when an incoming memory has a different
// normalized claim from an existing same-scope + same-authority peer, both
// the existing peer (re-loaded via idempotent re-Save) and the new row must
// land as StatusDisputed.
func TestSaveMarksBothDisputedOnSameScopeDifferentClaim(t *testing.T) {
	store := setupMemoryStore(t)
	s := memory.OpenMemory(store)

	scope := memory.Scope{Kind: "project", Value: "mengdie"}

	first, err := s.SaveUserMemory(context.Background(), memory.Memory{
		Claim:  "项目测试入口是 go test ./internal/foo",
		Scope:  scope,
		Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-1:1:user"},
	})
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	if first.Status != memory.StatusActive {
		t.Fatalf("first status=%s, want active", first.Status)
	}

	second, err := s.SaveUserMemory(context.Background(), memory.Memory{
		Claim:  "项目测试入口是 go test ./...",
		Scope:  scope,
		Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-1:2:user"},
	})
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if second.Status != memory.StatusDisputed {
		t.Fatalf("new row status=%s, want disputed", second.Status)
	}

	// Re-saving the original claim should hit the normalized-idempotency
	// path and return the peer row, which must now be StatusDisputed.
	reloaded, err := s.SaveUserMemory(context.Background(), memory.Memory{
		Claim:  "项目测试入口是 go test ./internal/foo",
		Scope:  scope,
		Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-1:3:user"},
	})
	if err != nil {
		t.Fatalf("reload save: %v", err)
	}
	if reloaded.Status != memory.StatusDisputed {
		t.Fatalf("peer status=%s, want disputed", reloaded.Status)
	}
	if reloaded.ID != first.ID {
		t.Fatalf("peer id=%s, want %s (idempotency broken)", reloaded.ID, first.ID)
	}
}

// TestSaveIsIdempotentUnderConflict locks in the contract that two
// concurrent writers in the same scope + authority cannot surface a
// UNIQUE-constraint failure to the caller: both must return the same id and
// no error. The race fix (INSERT ... ON CONFLICT(id) DO NOTHING RETURNING
// id) collapses the SELECT-then-INSERT race window, with the normalized
// SELECT-for-idempotency still catching the NFD/NFC same-claim case that
// ON CONFLICT(id) cannot detect on its own.
//
// The test opens two session stores against the same SQLite file (WAL mode
// permits multiple writers) and fans the same Save call out across many
// goroutines, alternating between stores. The actual race is hard to
// reproduce deterministically — both stores serialize at BEGIN IMMEDIATE
// and the loser's SELECT sees the winner's committed row — but the
// contract is observable: every goroutine observes the same id and no
// error.
func TestSaveIsIdempotentUnderConflict(t *testing.T) {
	directory := t.TempDir()
	storeA := openSharedMemoryStore(t, directory, 5*time.Second)
	t.Cleanup(func() {
		if err := storeA.Close(); err != nil {
			t.Errorf("close storeA: %v", err)
		}
	})
	storeB := openSharedMemoryStore(t, directory, 5*time.Second)
	t.Cleanup(func() {
		if err := storeB.Close(); err != nil {
			t.Errorf("close storeB: %v", err)
		}
	})

	memA := memory.OpenMemory(storeA)
	memB := memory.OpenMemory(storeB)

	in := memory.Memory{
		Claim:  "项目用 go test ./...",
		Scope:  memory.Scope{Kind: "project", Value: "mengdie"},
		Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-1:1:user"},
	}

	const concurrency = 32
	results := make([]struct {
		id  string
		err error
	}, concurrency)

	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		store := memA
		if i%2 == 1 {
			store = memB
		}
		go func(index int, target *memory.Store) {
			defer wg.Done()
			got, err := target.SaveUserMemory(context.Background(), in)
			results[index] = struct {
				id  string
				err error
			}{got.ID, err}
		}(i, store)
	}
	wg.Wait()

	expectedID := memory.GenerateID(in.Claim, in.Scope, string(memory.AuthorityExplicit), "session-1")
	for i, r := range results {
		if r.err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, r.err)
			continue
		}
		if r.id != expectedID {
			t.Errorf("goroutine %d: id=%s, want %s", i, r.id, expectedID)
		}
	}

	// Exactly one durable row should exist for this (claim, scope,
	// authority, sessionID) tuple — UNIQUE(id) plus the idempotency
	// contract together forbid duplicates.
	var rowCount int
	if err := storeA.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM memories WHERE id = ?`, expectedID,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("expected exactly one row with id=%s, got %d", expectedID, rowCount)
	}
}
