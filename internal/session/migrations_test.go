// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"testing"
	"time"
)

// TestMigration008MemoryApplied verifies that the 008_memory.sql migration
// creates the four tables the trusted-memory subsystem depends on:
// memories (with the existing rowid-based FTS5 wiring), the FTS5 virtual
// table memories_fts, and the two companion ledgers memory_evidence and
// memory_usage. The migration runs inside OpenSQLite, so this is also the
// first end-to-end check that the new migration integrates with the same
// schema_migrations ledger and checksum validation as the older seven.
func TestMigration008MemoryApplied(t *testing.T) {
	store, err := OpenSQLite(context.Background(), OpenOptions{
		DataDir: t.TempDir(), ProjectRoot: t.TempDir(), Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestStore(t, store)
	rows, err := store.DB().QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='table' AND name IN ('memories','memories_fts','memory_evidence','memory_usage')`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		seen[n] = true
	}
	for _, want := range []string{"memories", "memories_fts", "memory_evidence", "memory_usage"} {
		if !seen[want] {
			t.Fatalf("missing %s", want)
		}
	}
}
