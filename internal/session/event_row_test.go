// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
)

// TestEventsProjectionPrefersSourceCommand verifies that the Events() row
// projection prefers events.ToolCompleted.SourceCommand over events.ToolCompleted.Summary
// when both fields are populated. This is the contract that lets ruleGoTest
// fire on real shell invocations whose argument text was captured separately
// from the human-facing Summary.
//
// The test seeds one record through the lowest-level appender exposed by
// *SQLiteStore (Append) because SQLiteStore intentionally has no higher-level
// AppendEvent helper.
func TestEventsProjectionPrefersSourceCommand(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginTestRun(t, store)

	record := toolCompletedRecord(t, 1, 1, "evt-source-command", events.ToolCompleted{
		Tool:          "shell",
		Success:       true,
		Summary:       "完成",
		SourceCommand: "go test ./...",
	})
	if err := store.Append(context.Background(), "session-1", 0, []Record{record}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.Events(context.Background(), "session-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].SourceRef != "go test ./..." {
		t.Fatalf("want source_command %q, got %q", "go test ./...", rows[0].SourceRef)
	}
}

// TestEventsProjectionFallbackSummary verifies the legacy contract is
// preserved: when events.ToolCompleted carries no SourceCommand, the
// projection still surfaces the Summary string so existing records written
// before migration 009 remain observable to ruleGoTest / ruleGoLint.
func TestEventsProjectionFallbackSummary(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginTestRun(t, store)

	record := toolCompletedRecord(t, 1, 1, "evt-summary-fallback", events.ToolCompleted{
		Tool:    "shell",
		Success: true,
		Summary: "完成",
	})
	if err := store.Append(context.Background(), "session-1", 0, []Record{record}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.Events(context.Background(), "session-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].SourceRef != "完成" {
		t.Fatalf("want Summary fallback %q, got %q", "完成", rows[0].SourceRef)
	}
}

// toolCompletedRecord encodes one events.ToolCompleted payload as a
// session.Record using events.New so the JSON shape exactly matches what
// the production event path produces.
func toolCompletedRecord(t *testing.T, sessionSeq, runSeq uint64, id string, payload events.ToolCompleted) Record {
	t.Helper()
	event, err := events.New("run-1", runSeq, storeTestTime.Add(time.Duration(sessionSeq)*time.Second), events.KindToolCompleted, payload)
	if err != nil {
		t.Fatal(err)
	}
	raw := event.Payload
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	return Record{
		ID:            id,
		SessionID:     "session-1",
		SessionSeq:    sessionSeq,
		RunID:         "run-1",
		RunSeq:        runSeq,
		Kind:          string(events.KindToolCompleted),
		SchemaVersion: event.Version,
		Visibility:    VisibilityPublic,
		Payload:       raw,
		Time:          event.Time,
	}
}
