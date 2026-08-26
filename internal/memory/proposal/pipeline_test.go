// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package proposal_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/memory/proposal"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// openPipelineFixture wires the dependency graph the Pipeline needs (Task 2
// Reflect skeleton): session.SQLiteStore (raw event source),
// memory.Store (Stage 2 M3 Slice 02 extraction backstop), proposal.Store
// (Stage 5 INSERT target) and the Pipeline itself. ProjectRoot /
// DataDir come from paired t.TempDir() directories so each test gets an
// isolated, fully-migrated session SQLite database — the 010
// reflection_proposals migration runs as part of OpenSQLite.
func openPipelineFixture(t *testing.T) (*proposal.Pipeline, *memory.Store, *proposal.Store, *session.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	projectRoot := t.TempDir()
	sessionStore, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir:     dir,
		ProjectRoot: filepath.Join(projectRoot, "project"),
		Now:         func() time.Time { return proposalTestTime },
	})
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	memStore := memory.OpenMemory(sessionStore)
	propStore := proposal.Open(sessionStore.DB(), func() time.Time { return proposalTestTime })
	return proposal.New(sessionStore, memStore, propStore, func() time.Time { return proposalTestTime }),
		memStore, propStore, sessionStore
}

// seedSessionEvents appends a session.RunStarted -> events.ToolCompleted
// -> events.RunCompleted triple to the given session via the existing
// session.RunMetadata / Append path (the brief's prose mentions
// `sessionStore.AppendEvent` but the store exposes BeginRun + Append,
// which is what every other test in the project uses; this helper keeps
// the test self-contained without adding a new method). The seq sequence
// always starts at 1 and the clock is the test's fixed proposalTestTime so
// observed_at stamps are deterministic.
func seedSessionEvents(t *testing.T, store *session.SQLiteStore, sessionID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.BeginRun(ctx, session.RunMetadata{
		SessionID:   sessionID,
		RunID:       sessionID + "-run1",
		ProjectRoot: filepath.Join(t.TempDir(), "seed"),
		Provider:    "test",
		Model:       "test-model",
		StartedAt:   proposalTestTime,
	}); err != nil {
		t.Fatalf("BeginRun: %v", err)
	}

	record := func(seq int, kind string, payload any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %s: %v", kind, err)
		}
		rec := session.Record{
			ID:            "evt-" + sessionID + "-" + kind,
			SessionID:     sessionID,
			SessionSeq:    uint64(seq),
			RunID:         sessionID + "-run1",
			RunSeq:        uint64(seq),
			Kind:          kind,
			SchemaVersion: events.SchemaVersion,
			Visibility:    session.VisibilityPublic,
			Payload:       raw,
			Time:          proposalTestTime,
		}
		expectedSeq := uint64(seq - 1)
		if err := store.Append(ctx, sessionID, expectedSeq, []session.Record{rec}); err != nil {
			t.Fatalf("Append %d %s: %v", seq, kind, err)
		}
	}
	record(1, string(events.KindRunStarted), events.RunStarted{Model: "test-model", CWD: "/tmp", Security: "default"})
	record(2, string(events.KindToolCompleted), events.ToolCompleted{Tool: "shell", Success: true, Summary: "go test ./..."})
	record(3, string(events.KindRunCompleted), events.RunCompleted{Summary: "ok"})
}

// TestPipelineReflectEmptyReturnsNoProposals covers the no-input branch of
// Stage 1: a fresh pipeline + empty session store must surface zero
// proposals and zero rows in the reflection_proposals table. The empty
// pipeline is also a smoke test that the dependency graph constructed by
// openPipelineFixture is internally consistent.
func TestPipelineReflectEmptyReturnsNoProposals(t *testing.T) {
	ctx := context.Background()
	p, _, propStore, sessionStore := openPipelineFixture(t)
	defer func() { _ = sessionStore.Close() }()

	proposals, err := p.Reflect(ctx, proposal.ReflectOptions{MaxSessions: 5})
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if len(proposals) != 0 {
		t.Fatalf("want 0 proposals, got %d", len(proposals))
	}

	list, err := propStore.List(ctx, proposal.ListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("want 0 stored proposals, got %d", len(list))
	}
}

// TestPipelineReflectScansEvents covers Stage 1 (Scan) + Stage 2 (Extract)
// with a non-empty session: seeding 3 events through the standard
// session.BeginRun + Append path must let the pipeline run end-to-end
// without erroring. The test deliberately does not pin the exact proposal
// count — pattern-detection logic lands in Task 3 — but verifies that the
// pipeline wires scan / extract / verify / reflect / propose together
// with a populated events table.
func TestPipelineReflectScansEvents(t *testing.T) {
	ctx := context.Background()
	p, _, _, sessionStore := openPipelineFixture(t)
	defer func() { _ = sessionStore.Close() }()

	seedSessionEvents(t, sessionStore, "pipeline-test")

	proposals, err := p.Reflect(ctx, proposal.ReflectOptions{MaxSessions: 5})
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	t.Logf("got %d proposals", len(proposals))
	// Don't assert exact count — patterns depend on event details; Task 3
	// owns the detection logic. The pipeline shouldn't error on a session
	// with events.
}
