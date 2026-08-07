// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
)

func TestReduceBuildsPublicViewAndSkipsUnknownKinds(t *testing.T) {
	truth := true
	records := []Record{
		viewRecord(t, 1, events.KindRunStarted, events.RunStarted{Model: "model", Security: "safe"}),
		viewRecord(t, 2, events.KindMessageCompleted, events.MessageCompleted{Text: "完成"}),
		viewRecord(t, 3, events.KindTodoUpdated, events.TodoUpdated{Todos: []events.Todo{{ID: "1", Content: "test", Status: "completed"}}}),
		viewRecord(t, 4, events.KindToolProposed, events.ToolProposed{CallID: "call-1", Tool: "read_file", Effects: []string{"read"}}),
		viewRecord(t, 5, events.KindApprovalNeeded, events.ApprovalNeeded{CallID: "call-1", Prompt: "允许？", Risk: "low"}),
		viewRecord(t, 6, events.KindApprovalResolved, events.ApprovalResolved{CallID: "call-1", Decision: "allow"}),
		viewRecord(t, 7, events.KindToolStarted, events.ToolStarted{CallID: "call-1", Tool: "read_file"}),
		viewRecord(t, 8, events.KindToolCompleted, events.ToolCompleted{CallID: "call-1", Tool: "read_file", Success: truth, Summary: "ok", DurationMS: 3}),
		viewRecord(t, 9, events.KindUsageUpdated, events.UsageUpdated{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 2}),
		viewRecord(t, 10, events.KindWarning, events.Warning{Code: "W", Message: "warning"}),
		{ID: "evt-unknown", SessionID: "session-view", SessionSeq: 11, RunID: "run-view", RunSeq: 11, Kind: "future.fact", SchemaVersion: 1, Visibility: VisibilityPublic, Payload: json.RawMessage(`{"future":true}`), Time: storeTestTime.Add(11 * time.Second)},
		viewRecord(t, 12, events.KindRunCompleted, events.RunCompleted{Summary: "done"}),
	}
	view, err := Reduce(SessionView{ID: "session-view", Status: "active", CreatedAt: storeTestTime, UpdatedAt: storeTestTime}, records)
	if err != nil {
		t.Fatal(err)
	}
	if view.LastSeq != 12 || view.Status != "completed" || len(view.Runs) != 1 || view.Runs[0].Status != "completed" {
		t.Fatalf("view status=%s last=%d runs=%+v", view.Status, view.LastSeq, view.Runs)
	}
	if len(view.Messages) != 1 || view.Messages[0].Text != "完成" || len(view.Todos) != 1 || len(view.Tools) != 1 || len(view.Approvals) != 1 || len(view.Warnings) != 1 {
		t.Fatalf("view=%+v", view)
	}
	if view.Tools[0].Success == nil || !*view.Tools[0].Success || view.Tools[0].Phase != "completed" || view.Approvals[0].Decision != "allow" {
		t.Fatalf("tool=%+v approval=%+v", view.Tools[0], view.Approvals[0])
	}
	if view.Usage.InputTokens != 10 || view.Usage.OutputTokens != 5 || view.Usage.CacheReadTokens != 2 {
		t.Fatalf("usage=%+v", view.Usage)
	}
}

func TestSnapshotCASAndCorruptFallbackToFacts(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginTestRun(t, store)
	records := []Record{
		viewRecordForSession(t, "session-1", "run-1", 1, events.KindRunStarted, events.RunStarted{Model: "model"}),
		viewRecordForSession(t, "session-1", "run-1", 2, events.KindRunCompleted, events.RunCompleted{Summary: "done"}),
	}
	if err := store.Append(context.Background(), "session-1", 0, records); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.View(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshot(context.Background(), view, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshot(context.Background(), view, 0); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("SaveSnapshot(stale)=%v", err)
	}
	if _, err := store.db.Exec(`UPDATE snapshots SET state_sha256='sha256:broken' WHERE session_id='session-1'`); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := service.View(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.LastSeq != 2 || rebuilt.Status != "completed" {
		t.Fatalf("rebuilt=%+v", rebuilt)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM snapshots WHERE session_id='session-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("corrupt snapshot count=%d", count)
	}
	if err := store.SaveSnapshot(context.Background(), rebuilt, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE snapshots SET schema_version=99 WHERE session_id='session-1'`); err != nil {
		t.Fatal(err)
	}
	compatible, err := service.View(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if compatible.LastSeq != rebuilt.LastSeq || compatible.Status != rebuilt.Status {
		t.Fatalf("incompatible fallback=%+v", compatible)
	}
}

func TestServiceListFiltersProjectsAndDeleteCascadesPrivateData(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	root := filepath.Clean(t.TempDir())
	payload, err := TaskCommandPayload("secret task")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "session-command", CommandID: "command-1", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "run-command", ProjectRoot: root,
		Provider: "openai-compatible", Model: "model", StartedAt: storeTestTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.List(context.Background(), ListOptions{ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "session-command" {
		t.Fatalf("items=%+v", items)
	}
	other, err := service.List(context.Background(), ListOptions{ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("other=%+v", other)
	}
	if err := service.Delete(context.Background(), "session-command"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sessions", "commands", "runs", "events", "snapshots"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("table %s count=%d", table, count)
		}
	}
	if err := service.Delete(context.Background(), "session-command"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("delete missing=%v", err)
	}
}

func viewRecord(t *testing.T, sequence uint64, kind events.Kind, payload any) Record {
	t.Helper()
	return viewRecordForSession(t, "session-view", "run-view", sequence, kind, payload)
}

func viewRecordForSession(t *testing.T, sessionID, runID string, sequence uint64, kind events.Kind, payload any) Record {
	t.Helper()
	event, err := events.New(runID, sequence, storeTestTime.Add(time.Duration(sequence)*time.Second), kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	raw := event.Payload
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	return Record{
		ID: "evt-view-" + sessionID + "-" + time.Duration(sequence).String(), SessionID: sessionID,
		SessionSeq: sequence, RunID: runID, RunSeq: sequence, Kind: string(kind),
		SchemaVersion: event.Version, Visibility: VisibilityPublic, Payload: raw, Time: event.Time,
	}
}
