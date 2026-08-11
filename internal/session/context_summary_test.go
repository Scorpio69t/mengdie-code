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

	agentcontext "github.com/Scorpio69t/mengdie-code/internal/context"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

func TestContextSummaryPersistsAdvancingSourceRangeAndDetectsCorruption(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	root := filepath.Join(t.TempDir(), "project")
	recorder := beginSummaryTestRun(t, store, root)
	for _, message := range []provider.Message{
		{Role: provider.RoleUser, Content: "原始任务"},
		{Role: provider.RoleAssistant, Content: "旧事实一"},
		{Role: provider.RoleAssistant, Content: "旧事实二"},
		{Role: provider.RoleAssistant, Content: "最近事实一"},
		{Role: provider.RoleAssistant, Content: "最近事实二"},
	} {
		if err := recorder.RecordMessage(context.Background(), message, true); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := recorder.RecordCompaction(context.Background(), agentcontext.CompactionRecord{
		Summary: summaryTestDocument("第一版摘要"), RetainedTailMessages: 2,
		GeneratorModel: "test-model", GeneratorVersion: agentcontext.SummaryProtocolVersion,
		EstimatedBefore: 5000, EstimatedAfterUpperBound: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SourceStart != 2 || receipt.SourceEnd != 3 {
		t.Fatalf("receipt=%+v", receipt)
	}
	loaded, err := store.LoadLatestContextSummary(context.Background(), "summary-session")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Summary != summaryTestDocument("第一版摘要") || loaded.SourceEnd != 3 || loaded.SHA256 == "" {
		t.Fatalf("summary=%+v", loaded)
	}
	if err := recorder.RecordMessage(context.Background(), provider.Message{Role: provider.RoleAssistant, Content: "新增最近事实"}, true); err != nil {
		t.Fatal(err)
	}
	receipt, err = recorder.RecordCompaction(context.Background(), agentcontext.CompactionRecord{
		Summary: summaryTestDocument("第二版摘要"), RetainedTailMessages: 2,
		GeneratorModel: "test-model", GeneratorVersion: agentcontext.SummaryProtocolVersion,
		EstimatedBefore: 6000, EstimatedAfterUpperBound: 2100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SourceEnd != 4 {
		t.Fatalf("receipt=%+v", receipt)
	}
	if _, err := recorder.RecordCompaction(context.Background(), agentcontext.CompactionRecord{
		Summary: summaryTestDocument("不前进"), RetainedTailMessages: 2,
		GeneratorModel: "test-model", GeneratorVersion: agentcontext.SummaryProtocolVersion,
		EstimatedBefore: 6000, EstimatedAfterUpperBound: 2100,
	}); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("non-advancing RecordCompaction() error=%v", err)
	}
	if _, err := store.db.Exec(`UPDATE context_summaries SET summary_text='tampered' WHERE session_id='summary-session' AND source_end=4`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadLatestContextSummary(context.Background(), "summary-session"); !errors.Is(err, ErrContextCorrupt) {
		t.Fatalf("LoadLatestContextSummary() error=%v", err)
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), "summary-session"); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM context_summaries WHERE session_id='summary-session'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("context summaries remaining=%d", remaining)
	}
}

func TestAnalyzeResumeUsesVerifiedSummaryAndOriginalTail(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	recorder := beginSummaryTestRun(t, store, root)
	call := provider.ToolCall{ID: "call-1", Type: "function", Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)}
	for _, message := range []provider.Message{
		{Role: provider.RoleUser, Content: "原始任务"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Name, Content: "读取结果"},
		{Role: provider.RoleAssistant, Content: "最近结论"},
	} {
		if err := recorder.RecordMessage(context.Background(), message, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := recorder.RecordCompaction(context.Background(), agentcontext.CompactionRecord{
		Summary: summaryTestDocument("已读取 a.txt，仍需继续验证。"), RetainedTailMessages: 1,
		GeneratorModel: "test-model", GeneratorVersion: agentcontext.SummaryProtocolVersion,
		EstimatedBefore: 5000, EstimatedAfterUpperBound: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	records := []Record{
		resumeTestRecord(t, "summary-session", "summary-run", "summary-command", 1, 1, events.KindRunStarted, events.RunStarted{Model: "test-model"}),
		resumeTestRecord(t, "summary-session", "summary-run", "summary-command", 2, 2, events.KindMessageCompleted, events.MessageCompleted{}),
		resumeTestRecord(t, "summary-session", "summary-run", "summary-command", 3, 3, events.KindToolProposed, events.ToolProposed{CallID: call.ID, Tool: call.Name, Effects: []string{"read"}}),
		resumeTestRecord(t, "summary-session", "summary-run", "summary-command", 4, 4, events.KindToolStarted, events.ToolStarted{CallID: call.ID, Tool: call.Name}),
		resumeTestRecord(t, "summary-session", "summary-run", "summary-command", 5, 5, events.KindToolCompleted, events.ToolCompleted{CallID: call.ID, Tool: call.Name, Success: true, Summary: "ok"}),
		resumeTestRecord(t, "summary-session", "summary-run", "summary-command", 6, 6, events.KindMessageCompleted, events.MessageCompleted{Text: "最近结论"}),
		resumeTestRecord(t, "summary-session", "summary-run", "summary-command", 7, 7, events.KindRunCompleted, events.RunCompleted{Summary: "最近结论"}),
	}
	if err := store.Append(context.Background(), "summary-session", 0, records); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.AnalyzeResume(context.Background(), "summary-session", root)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanResume || plan.ContextSummary == "" || len(plan.History) != 2 ||
		plan.History[0].Content != "原始任务" || plan.History[1].Content != "最近结论" {
		t.Fatalf("plan=%+v history=%+v", plan, plan.History)
	}
	if _, err := store.db.Exec(`UPDATE context_summaries SET summary_text='tampered' WHERE session_id='summary-session'`); err != nil {
		t.Fatal(err)
	}
	blocked, err := service.AnalyzeResume(context.Background(), "summary-session", root)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.CanResume || !strings.Contains(blocked.Reason, "滚动摘要损坏") {
		t.Fatalf("blocked plan=%+v", blocked)
	}
}

func beginSummaryTestRun(t *testing.T, store *SQLiteStore, root string) *ContextRecorder {
	t.Helper()
	payload, err := TaskCommandPayload("原始任务")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "summary-session", CommandID: "summary-command", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "summary-run", ProjectRoot: root,
		Provider: "openai-compatible", Model: "test-model", StartedAt: storeTestTime,
	}); err != nil {
		t.Fatal(err)
	}
	recorder, err := store.NewContextRecorder(context.Background(), "summary-session", "summary-run", "summary-command")
	if err != nil {
		t.Fatal(err)
	}
	return recorder
}

func summaryTestDocument(value string) string {
	encoded, err := json.Marshal(map[string][]string{
		"objective_and_constraints": {value},
		"decisions":                 {}, "verified_evidence": {"测试证据"}, "unresolved_errors": {},
		"todo_approval_tool_state": {}, "continuation_pointers": {"继续验证"},
	})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
