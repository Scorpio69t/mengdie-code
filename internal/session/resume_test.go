// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func TestAnalyzeResumeUsesPatchJournalForInterruptedWrite(t *testing.T) {
	tests := []struct {
		name         string
		current      string
		canResume    bool
		recoveryKind string
		reason       string
	}{
		{name: "pre retries only after approval", current: "before\n", canResume: true, recoveryKind: RecoveryRetryWrite},
		{name: "post is acknowledged without replay", current: "after\n", canResume: true, recoveryKind: RecoveryVerifyWrite},
		{name: "neither blocks", current: "manual edit\n", reason: "既不匹配"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t, t.TempDir(), 0)
			defer closeTestStore(t, store)
			guard, err := platform.NewPathGuard(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := guard.Root()
			path := filepath.Join(root, "value.txt")
			if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			seedInterruptedWriteSession(t, store, root, path)
			if err := os.WriteFile(path, []byte(test.current), 0o600); err != nil {
				t.Fatal(err)
			}
			service, err := NewService(store)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := service.AnalyzeResume(context.Background(), "session-resume", root)
			if err != nil {
				t.Fatal(err)
			}
			if plan.CanResume != test.canResume {
				t.Fatalf("plan=%+v", plan)
			}
			if test.canResume {
				if plan.Recovery == nil || plan.Recovery.Kind != test.recoveryKind {
					t.Fatalf("recovery=%+v", plan.Recovery)
				}
			} else if !strings.Contains(plan.Reason, test.reason) {
				t.Fatalf("reason=%q", plan.Reason)
			}
		})
	}
}

func TestAnalyzeResumeAndBeginResumeCommandRun(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	root := filepath.Clean(t.TempDir())
	seedCompletedResumeSession(t, store, root)
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.AnalyzeResume(context.Background(), "session-resume", root)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanResume || len(plan.History) != 2 || plan.LastSeq != 3 || plan.ContextOrdinal != 2 {
		t.Fatalf("plan=%+v history=%+v", plan, plan.History)
	}
	payload, err := ResumeCommandPayload(plan.SessionID, "继续验证")
	if err != nil {
		t.Fatal(err)
	}
	metadata := ResumeCommandRunMetadata{CommandRunMetadata: CommandRunMetadata{
		SessionID: plan.SessionID, CommandID: "resume-command", CommandKind: CommandKindResume,
		CommandPayload: payload, RunID: "run-resume-2", ProjectRoot: root,
		Provider: "openai-compatible", Model: "model", StartedAt: storeTestTime.Add(time.Hour),
	}, ExpectedSessionSeq: plan.ExpectedSessionSeq, ExpectedContextOrdinal: plan.ExpectedContextOrdinal}
	begin, err := store.BeginResumeCommandRun(context.Background(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	if begin.Existing || begin.AfterSeq != 3 || begin.Command.SessionID != plan.SessionID {
		t.Fatalf("begin=%+v", begin)
	}
	matched, err := service.MatchResumeCommand(context.Background(), "resume-command", plan.SessionID, "继续验证", root)
	if err != nil || !matched {
		t.Fatalf("MatchResumeCommand() matched=%t error=%v", matched, err)
	}
	if _, err := service.MatchResumeCommand(context.Background(), "resume-command", plan.SessionID, "另一条指令", root); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("MatchResumeCommand(conflict)=%v", err)
	}
	repeated, err := store.BeginResumeCommandRun(context.Background(), ResumeCommandRunMetadata{
		CommandRunMetadata: CommandRunMetadata{
			SessionID: plan.SessionID, CommandID: "resume-command", CommandKind: CommandKindResume,
			CommandPayload: json.RawMessage(`{"message":"继续验证","session_id":"session-resume"}`),
			RunID:          "ignored", ProjectRoot: root, Provider: "other", Model: "other", StartedAt: storeTestTime,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.Existing || repeated.RunID != "run-resume-2" {
		t.Fatalf("repeated=%+v", repeated)
	}
	conflictMetadata := metadata
	conflictMetadata.CommandPayload, _ = ResumeCommandPayload(plan.SessionID, "另一条指令")
	if _, err := store.BeginResumeCommandRun(context.Background(), conflictMetadata); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("payload conflict=%v", err)
	}

	recorder, err := store.NewContextRecorder(context.Background(), plan.SessionID, "run-resume-2", "resume-command")
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessage(context.Background(), provider.Message{Role: provider.RoleUser, Content: "继续验证"}, true); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessage(context.Background(), provider.Message{Role: provider.RoleAssistant, Content: "第二轮完成"}, true); err != nil {
		t.Fatal(err)
	}
	records := []Record{
		resumeTestRecord(t, plan.SessionID, "run-resume-2", "resume-command", 4, 1, events.KindRunStarted, events.RunStarted{Model: "model"}),
		resumeTestRecord(t, plan.SessionID, "run-resume-2", "resume-command", 5, 2, events.KindMessageCompleted, events.MessageCompleted{Text: "第二轮完成"}),
		resumeTestRecord(t, plan.SessionID, "run-resume-2", "resume-command", 6, 3, events.KindRunCompleted, events.RunCompleted{Summary: "第二轮完成"}),
	}
	if err := store.Append(context.Background(), plan.SessionID, 3, records); err != nil {
		t.Fatal(err)
	}
	view, err := service.View(context.Background(), plan.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Runs) != 2 || len(view.Messages) != 2 || view.Status != "completed" {
		t.Fatalf("multi-run view=%+v", view)
	}
}

func TestBeginResumeCommandRunRejectsStaleAnalyzerPositions(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	root := filepath.Clean(t.TempDir())
	seedCompletedResumeSession(t, store, root)
	payload, _ := ResumeCommandPayload("session-resume", "继续")
	metadata := ResumeCommandRunMetadata{CommandRunMetadata: CommandRunMetadata{
		SessionID: "session-resume", CommandID: "resume-stale", CommandKind: CommandKindResume,
		CommandPayload: payload, RunID: "run-stale", ProjectRoot: root,
		Provider: "openai-compatible", Model: "model", StartedAt: storeTestTime,
	}, ExpectedSessionSeq: 2, ExpectedContextOrdinal: 2}
	if _, err := store.BeginResumeCommandRun(context.Background(), metadata); err == nil {
		t.Fatal("stale session sequence unexpectedly accepted")
	} else {
		var conflict *SequenceConflictError
		if !errors.As(err, &conflict) || conflict.Actual != 3 {
			t.Fatalf("stale sequence error=%v", err)
		}
	}
	metadata.ExpectedSessionSeq = 3
	metadata.ExpectedContextOrdinal = 1
	if _, err := store.BeginResumeCommandRun(context.Background(), metadata); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("stale context error=%v", err)
	}
}

func TestAnalyzeResumeFailsClosedForOldOrIncompleteBoundaries(t *testing.T) {
	for name, test := range map[string]struct {
		seed func(*testing.T, *SQLiteStore, string)
		want string
	}{
		"old session": {
			seed: func(t *testing.T, store *SQLiteStore, root string) {
				seedCompletedResumeSession(t, store, root)
				if _, err := store.db.Exec(`DELETE FROM context_messages WHERE session_id='session-resume'`); err != nil {
					t.Fatal(err)
				}
			}, want: "没有私有上下文日志",
		},
		"pending tool": {
			seed: seedPendingToolResumeSession, want: "状态为 proposed",
		},
		"private public mismatch": {
			seed: func(t *testing.T, store *SQLiteStore, root string) {
				seedCompletedResumeSession(t, store, root)
				if _, err := store.db.Exec(`DELETE FROM events WHERE session_id='session-resume' AND kind='message.completed'`); err != nil {
					t.Fatal(err)
				}
			}, want: "不一致",
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := openTestStore(t, t.TempDir(), 0)
			defer closeTestStore(t, store)
			root := filepath.Clean(t.TempDir())
			test.seed(t, store, root)
			service, _ := NewService(store)
			plan, err := service.AnalyzeResume(context.Background(), "session-resume", root)
			if err != nil {
				t.Fatal(err)
			}
			if plan.CanResume || !strings.Contains(plan.Reason, test.want) || len(plan.History) != 0 {
				t.Fatalf("plan=%+v", plan)
			}
		})
	}
}

func TestAnalyzeResumeSelectsOnlySafeInterruptedRecovery(t *testing.T) {
	for name, test := range map[string]struct {
		effects  []string
		approval bool
		want     string
		blocked  bool
	}{
		"pending approval is reapproved":    {effects: []string{"write"}, approval: true, want: RecoveryReapprove},
		"in flight read retries":            {effects: []string{"read"}, want: RecoveryRetryRead},
		"in flight state retries":           {effects: []string{"state"}, want: RecoveryRetryRead},
		"in flight write remains blocked":   {effects: []string{"write"}, blocked: true},
		"in flight command remains blocked": {effects: []string{"execute"}, blocked: true},
		"in flight network remains blocked": {effects: []string{"network"}, blocked: true},
	} {
		t.Run(name, func(t *testing.T) {
			store := openTestStore(t, t.TempDir(), 0)
			defer closeTestStore(t, store)
			root := filepath.Clean(t.TempDir())
			seedInterruptedRecoverySession(t, store, root, test.effects, test.approval)
			service, err := NewService(store)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := service.AnalyzeResume(context.Background(), "session-resume", root)
			if err != nil {
				t.Fatal(err)
			}
			if test.blocked {
				if plan.CanResume || !strings.Contains(plan.Reason, "保持阻断") {
					t.Fatalf("plan=%+v", plan)
				}
				return
			}
			if !plan.CanResume || plan.Recovery == nil || plan.Recovery.Kind != test.want ||
				plan.Recovery.SourceRunID != "run-resume-1" || plan.Recovery.Call.Name != "read_file" {
				t.Fatalf("plan=%+v", plan)
			}
		})
	}
}

func TestAnalyzeResumeBlocksMultipleInterruptedCalls(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	root := filepath.Clean(t.TempDir())
	seedInterruptedRecoverySession(t, store, root, []string{"read"}, false)
	if err := store.Append(context.Background(), "session-resume", 4, []Record{
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 5, 5, events.KindToolProposed, events.ToolProposed{CallID: "call-2", Tool: "read_file", Effects: []string{"read"}}),
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 6, 6, events.KindToolStarted, events.ToolStarted{CallID: "call-2", Tool: "read_file"}),
	}); err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(store)
	plan, err := service.AnalyzeResume(context.Background(), "session-resume", root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanResume || !strings.Contains(plan.Reason, "多个未完成") {
		t.Fatalf("plan=%+v", plan)
	}
}

func seedCompletedResumeSession(t *testing.T, store *SQLiteStore, root string) {
	t.Helper()
	payload, _ := TaskCommandPayload("初始任务")
	_, err := store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "session-resume", CommandID: "command-initial", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "run-resume-1", ProjectRoot: root,
		Provider: "openai-compatible", Model: "model", StartedAt: storeTestTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := store.NewContextRecorder(context.Background(), "session-resume", "run-resume-1", "command-initial")
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessage(context.Background(), provider.Message{Role: provider.RoleUser, Content: "初始任务"}, true); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessage(context.Background(), provider.Message{Role: provider.RoleAssistant, Content: "第一轮完成"}, true); err != nil {
		t.Fatal(err)
	}
	records := []Record{
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 1, 1, events.KindRunStarted, events.RunStarted{Model: "model"}),
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 2, 2, events.KindMessageCompleted, events.MessageCompleted{Text: "第一轮完成"}),
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 3, 3, events.KindRunCompleted, events.RunCompleted{Summary: "第一轮完成"}),
	}
	if err := store.Append(context.Background(), "session-resume", 0, records); err != nil {
		t.Fatal(err)
	}
}

func seedPendingToolResumeSession(t *testing.T, store *SQLiteStore, root string) {
	t.Helper()
	payload, _ := TaskCommandPayload("初始任务")
	_, err := store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "session-resume", CommandID: "command-initial", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "run-resume-1", ProjectRoot: root,
		Provider: "openai-compatible", Model: "model", StartedAt: storeTestTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, _ := store.NewContextRecorder(context.Background(), "session-resume", "run-resume-1", "command-initial")
	_ = recorder.RecordMessage(context.Background(), provider.Message{Role: provider.RoleUser, Content: "初始任务"}, true)
	_ = recorder.RecordMessage(context.Background(), provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
		ID: "call-1", Type: "function", Name: "edit_file", Arguments: json.RawMessage(`{"path":"a.txt"}`),
	}}}, true)
	records := []Record{
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 1, 1, events.KindRunStarted, events.RunStarted{Model: "model"}),
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 2, 2, events.KindMessageCompleted, events.MessageCompleted{}),
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 3, 3, events.KindToolProposed, events.ToolProposed{CallID: "call-1", Tool: "edit_file", Effects: []string{"write"}}),
	}
	if err := store.Append(context.Background(), "session-resume", 0, records); err != nil {
		t.Fatal(err)
	}
}

func seedInterruptedWriteSession(t *testing.T, store *SQLiteStore, root, path string) {
	t.Helper()
	payload, err := TaskCommandPayload("修改文件")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "session-resume", CommandID: "command-initial", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "run-resume-1", ProjectRoot: root,
		Provider: "openai-compatible", Model: "model", StartedAt: storeTestTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	arguments := json.RawMessage(`{"path":"value.txt","old_text":"before","new_text":"after"}`)
	call := provider.ToolCall{ID: "call-1", Type: "function", Name: "edit_file", Arguments: arguments}
	contextRecorder, err := store.NewContextRecorder(context.Background(), "session-resume", "run-resume-1", "command-initial")
	if err != nil {
		t.Fatal(err)
	}
	if err := contextRecorder.RecordMessage(context.Background(), provider.Message{Role: provider.RoleUser, Content: "修改文件"}, true); err != nil {
		t.Fatal(err)
	}
	if err := contextRecorder.RecordMessage(context.Background(), provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}}, true); err != nil {
		t.Fatal(err)
	}
	canonical, err := tools.Canonicalize(arguments)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.NewPatchJournalRecorder(context.Background(), "session-resume", "run-resume-1", "command-initial", root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Prepare(context.Background(), tools.MutationIntent{
		ToolCallID: call.ID, ToolName: call.Name, CallDigest: tools.ComputeDigest(call.Name, canonical),
		Path: path, PreExists: true, PreSHA256: bytesDigest("before\n"), PreMode: info.Mode().Perm(), PreContent: []byte("before\n"),
		PostExists: true, PostSHA256: bytesDigest("after\n"), PostMode: info.Mode().Perm(),
	}); err != nil {
		t.Fatal(err)
	}
	records := []Record{
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 1, 1, events.KindRunStarted, events.RunStarted{Model: "model"}),
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 2, 2, events.KindMessageCompleted, events.MessageCompleted{}),
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 3, 3, events.KindToolProposed, events.ToolProposed{CallID: call.ID, Tool: call.Name, Effects: []string{"write"}}),
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 4, 4, events.KindToolStarted, events.ToolStarted{CallID: call.ID, Tool: call.Name}),
	}
	if err := store.Append(context.Background(), "session-resume", 0, records); err != nil {
		t.Fatal(err)
	}
}

func seedInterruptedRecoverySession(t *testing.T, store *SQLiteStore, root string, effects []string, approval bool) {
	t.Helper()
	payload, _ := TaskCommandPayload("初始任务")
	_, err := store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "session-resume", CommandID: "command-initial", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "run-resume-1", ProjectRoot: root,
		Provider: "openai-compatible", Model: "model", StartedAt: storeTestTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := store.NewContextRecorder(context.Background(), "session-resume", "run-resume-1", "command-initial")
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessage(context.Background(), provider.Message{Role: provider.RoleUser, Content: "初始任务"}, true); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessage(context.Background(), provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
		ID: "call-1", Type: "function", Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`),
	}}}, true); err != nil {
		t.Fatal(err)
	}
	records := []Record{
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 1, 1, events.KindRunStarted, events.RunStarted{Model: "model"}),
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 2, 2, events.KindMessageCompleted, events.MessageCompleted{}),
		resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 3, 3, events.KindToolProposed, events.ToolProposed{CallID: "call-1", Tool: "read_file", Effects: effects}),
	}
	if approval {
		records = append(records, resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 4, 4, events.KindApprovalNeeded, events.ApprovalNeeded{CallID: "call-1", Prompt: "确认"}))
	} else {
		records = append(records, resumeTestRecord(t, "session-resume", "run-resume-1", "command-initial", 4, 4, events.KindToolStarted, events.ToolStarted{CallID: "call-1", Tool: "read_file"}))
	}
	if err := store.Append(context.Background(), "session-resume", 0, records); err != nil {
		t.Fatal(err)
	}
}

func resumeTestRecord(t *testing.T, sessionID, runID, commandID string, sessionSeq, runSeq uint64, kind events.Kind, payload any) Record {
	t.Helper()
	event, err := events.New(runID, runSeq, storeTestTime.Add(time.Duration(sessionSeq)*time.Second), kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	raw := event.Payload
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	return Record{
		ID: "evt-resume-" + runID + "-" + time.Duration(runSeq).String(), SessionID: sessionID,
		SessionSeq: sessionSeq, RunID: runID, RunSeq: runSeq, CommandID: commandID,
		Kind: string(kind), SchemaVersion: event.Version, Visibility: VisibilityPublic, Payload: raw, Time: event.Time,
	}
}
