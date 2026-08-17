// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/cost"
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
		viewRecord(t, 9, events.KindRecoveryResolved, events.RecoveryResolved{SourceRunID: "run-old", CallID: "call-1", Action: "retry_read", Outcome: "completed"}),
		viewRecord(t, 10, events.KindUsageUpdated, events.UsageUpdated{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 2}),
		viewRecord(t, 11, events.KindWarning, events.Warning{Code: "W", Message: "warning"}),
		{ID: "evt-unknown", SessionID: "session-view", SessionSeq: 12, RunID: "run-view", RunSeq: 12, Kind: "future.fact", SchemaVersion: 1, Visibility: VisibilityPublic, Payload: json.RawMessage(`{"future":true}`), Time: storeTestTime.Add(12 * time.Second)},
		viewRecord(t, 13, events.KindRunCompleted, events.RunCompleted{Summary: "done"}),
	}
	view, err := Reduce(SessionView{ID: "session-view", Status: "active", CreatedAt: storeTestTime, UpdatedAt: storeTestTime}, records)
	if err != nil {
		t.Fatal(err)
	}
	if view.LastSeq != 13 || view.Status != "completed" || len(view.Runs) != 1 || view.Runs[0].Status != "completed" {
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
	if len(view.Recoveries) != 1 || view.Recoveries[0].SourceRunID != "run-old" || view.Recoveries[0].Action != "retry_read" {
		t.Fatalf("recoveries=%+v", view.Recoveries)
	}
}

func TestReduceKeepsSameToolCallIDIndependentAcrossRuns(t *testing.T) {
	records := []Record{
		viewRecordForSession(t, "session-view", "run-one", 1, events.KindRunStarted, events.RunStarted{Model: "model"}),
		viewRecordForSession(t, "session-view", "run-one", 2, events.KindToolProposed, events.ToolProposed{CallID: "call-1", Tool: "read_file"}),
		viewRecordForSession(t, "session-view", "run-one", 3, events.KindToolCompleted, events.ToolCompleted{CallID: "call-1", Tool: "read_file", Success: true}),
		viewRecordForSession(t, "session-view", "run-one", 4, events.KindRunCompleted, events.RunCompleted{}),
		viewRecordForSession(t, "session-view", "run-two", 5, events.KindRunStarted, events.RunStarted{Model: "model"}),
		viewRecordForSession(t, "session-view", "run-two", 6, events.KindToolProposed, events.ToolProposed{CallID: "call-1", Tool: "shell"}),
		viewRecordForSession(t, "session-view", "run-two", 7, events.KindToolCompleted, events.ToolCompleted{CallID: "call-1", Tool: "shell", Success: false}),
		viewRecordForSession(t, "session-view", "run-two", 8, events.KindRunCompleted, events.RunCompleted{}),
	}
	view, err := Reduce(SessionView{ID: "session-view", Status: "active"}, records)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Tools) != 2 || view.Tools[0].RunID != "run-one" || view.Tools[1].RunID != "run-two" || view.Tools[1].Tool != "shell" {
		t.Fatalf("tools=%+v", view.Tools)
	}
}

func TestReduceAggregatesVersionedCostsAndExplicitUnknowns(t *testing.T) {
	records := []Record{
		viewRecord(t, 1, events.KindUsageUpdated, events.UsageUpdated{
			Purpose: "agent", RequestCount: 1, UsageReported: true,
			InputTokens: 10, OutputTokens: 2, TotalTokens: 12, CacheReadTokens: 3,
			ProviderOrigin: "https://api.deepseek.com", Model: "deepseek-v4-flash",
			CostStatus: cost.StatusEstimated, EstimatedCostPicoUSD: 1_000_000,
			Currency: cost.CurrencyUSD, PriceTableVersion: cost.TableVersion, PricingSource: "official",
		}),
		viewRecord(t, 2, events.KindUsageUpdated, events.UsageUpdated{
			Purpose: "context_summary", RequestCount: 1,
			Model:      "deepseek-v4-flash",
			CostStatus: cost.StatusUnknown, PriceTableVersion: cost.TableVersion,
			CostUnknownReason: cost.UnknownUsageUnreported,
		}),
	}
	view, err := Reduce(SessionView{ID: "session-view"}, records)
	if err != nil {
		t.Fatal(err)
	}
	if view.Usage.RequestCount != 2 || view.Usage.UsageReportedRequests != 1 ||
		view.Usage.InputTokens != 10 || view.Usage.TotalTokens != 12 || view.Usage.EstimatedCostPicoUSD != 1_000_000 ||
		view.Usage.EstimatedCostRequests != 1 || view.Usage.UnknownCostRequests != 1 {
		t.Fatalf("usage=%+v", view.Usage)
	}
	if len(view.Usage.PriceTableVersions) != 1 || view.Usage.PriceTableVersions[0] != cost.TableVersion ||
		len(view.Usage.CostUnknownReasons) != 1 || view.Usage.CostUnknownReasons[0] != cost.UnknownUsageUnreported {
		t.Fatalf("usage metadata=%+v", view.Usage)
	}
}

func TestReduceRejectsMalformedUsageFacts(t *testing.T) {
	record := viewRecord(t, 1, events.KindUsageUpdated, events.UsageUpdated{
		Purpose: "agent", RequestCount: 1, InputTokens: 1, Model: "model",
		CostStatus: cost.StatusUnknown, CostUnknownReason: cost.UnknownUsageUnreported,
	})
	if _, err := Reduce(SessionView{ID: "session-view"}, []Record{record}); err == nil || !strings.Contains(err.Error(), "unreported usage") {
		t.Fatalf("Reduce() error=%v", err)
	}
}

func TestSnapshotCASAndCorruptFallbackToFacts(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginTestRun(t, store)
	records := []Record{
		viewRecordForSession(t, "session-1", "run-1", 1, events.KindRunStarted, events.RunStarted{Model: "model"}),
		viewRecordForSession(t, "session-1", "run-1", 2, events.KindUsageUpdated, events.UsageUpdated{
			Purpose: "agent", RequestCount: 1, Model: "model", CostStatus: cost.StatusUnknown,
			PriceTableVersion: cost.TableVersion, CostUnknownReason: cost.UnknownUsageUnreported,
		}),
		viewRecordForSession(t, "session-1", "run-1", 3, events.KindRunCompleted, events.RunCompleted{Summary: "done"}),
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
	if rebuilt.LastSeq != 3 || rebuilt.Status != "completed" || rebuilt.Usage.RequestCount != 1 || rebuilt.Usage.UnknownCostRequests != 1 {
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

func TestReduceProjectsContextCompactionMetadataOnly(t *testing.T) {
	record := viewRecord(t, 1, events.KindContextCompacted, events.ContextCompacted{
		SourceStart: 2, SourceEnd: 9, EstimatedBefore: 12000,
		EstimatedAfterUpperBound: 8000, GeneratorModel: "model", GeneratorVersion: "protocol/v1",
	})
	view, err := Reduce(SessionView{ID: "session-view", Status: "active", CreatedAt: storeTestTime, UpdatedAt: storeTestTime}, []Record{record})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Compactions) != 1 || view.Compactions[0].SourceEnd != 9 || view.Compactions[0].GeneratorModel != "model" {
		t.Fatalf("compactions=%+v", view.Compactions)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "summary_text") {
		t.Fatalf("public projection leaked private summary: %s", encoded)
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
