// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package memory_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// memoryTestTime is the fixed clock used by Store tests. The deterministic
// stamp lets us compare UpdatedAt against expected RFC3339Nano values without
// flakiness and exercises the injectable Now field described in the brief.
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

// TestSaveUserMemoryCreatesActive covers the Authority-explicit happy path:
// a memory with AuthorityExplicit + SourceTypeUserMessage must be persisted
// as StatusActive (not proposed) and a deterministic id must be assigned.
// Step 1 of the brief pins this exact assertion before the Store is wired.
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
	if got.ID == "" {
		t.Fatal("id empty")
	}
}

// TestProposeMemoryAlwaysProposed covers the inferred path: even when the
// caller does not explicitly request a proposed status, ProposeMemory must
// return StatusProposed (spec §4.1: inferred memories cannot become active
// without an explicit user approval step).
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
}
