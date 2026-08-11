// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentcontext "github.com/Scorpio69t/mengdie-code/internal/context"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

type scriptedProvider struct {
	responses []*provider.ChatResponse
	errors    []error
	requests  []provider.ChatRequest
}

type recordedContext struct {
	message  provider.Message
	complete bool
}

type memoryContextRecorder struct {
	records     []recordedContext
	compactions []agentcontext.CompactionRecord
	errAt       int
	position    int
}

type memoryMutationJournal struct {
	intents map[string]tools.MutationIntent
	next    int
}

func (journal *memoryMutationJournal) Prepare(_ context.Context, intent tools.MutationIntent) (tools.MutationReceipt, error) {
	journal.next++
	id := fmt.Sprintf("journal-%d", journal.next)
	journal.intents[id] = intent
	return tools.MutationReceipt{JournalID: id}, nil
}

func (*memoryMutationJournal) MarkApplied(context.Context, tools.MutationReceipt) error { return nil }

func (journal *memoryMutationJournal) VerifyPost(_ context.Context, receipt tools.MutationReceipt) error {
	intent := journal.intents[receipt.JournalID]
	digest, err := tools.FileSHA256(intent.Path)
	if err != nil {
		return err
	}
	if digest != intent.PostSHA256 {
		return tools.ErrMutationConflict
	}
	return nil
}

func (recorder *memoryContextRecorder) RecordCompaction(_ context.Context, record agentcontext.CompactionRecord) (agentcontext.CompactionReceipt, error) {
	recorder.compactions = append(recorder.compactions, record)
	return agentcontext.CompactionReceipt{SourceStart: 2, SourceEnd: uint64(recorder.position - record.RetainedTailMessages)}, nil
}

func (recorder *memoryContextRecorder) RecordMessage(_ context.Context, message provider.Message, complete bool) error {
	if recorder.errAt > 0 && len(recorder.records)+1 == recorder.errAt {
		return errors.New("context store unavailable")
	}
	recorder.records = append(recorder.records, recordedContext{message: message, complete: complete})
	recorder.position++
	return nil
}

func (fake *scriptedProvider) ID() string { return "fake" }

func (fake *scriptedProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{ToolCalling: true, UsageInStream: true, StrictToolSchema: true, MaxContextTokens: 64_000}, nil
}

func (fake *scriptedProvider) Stream(ctx context.Context, request provider.ChatRequest, sink provider.StreamSink) (*provider.ChatResponse, error) {
	fake.requests = append(fake.requests, request)
	index := len(fake.requests) - 1
	if index < len(fake.errors) && fake.errors[index] != nil {
		return nil, fake.errors[index]
	}
	if index >= len(fake.responses) {
		return nil, errors.New("fake provider response exhausted")
	}
	response := fake.responses[index]
	if response.Message.Content != "" {
		if err := sink.OnEvent(ctx, provider.StreamEvent{Kind: provider.StreamTextDelta, Text: response.Message.Content}); err != nil {
			return nil, err
		}
	}
	if response.Usage != (provider.Usage{}) {
		usage := response.Usage
		if err := sink.OnEvent(ctx, provider.StreamEvent{Kind: provider.StreamUsage, Usage: &usage}); err != nil {
			return nil, err
		}
	}
	return response, nil
}

func TestAgentCompletesReadEditTestAndTodoLoop(t *testing.T) {
	root := t.TempDir()
	writeAgentFixture(t, root)
	fake := &scriptedProvider{responses: []*provider.ChatResponse{
		assistantTool("read", "read_file", map[string]any{"path": "value.go"}),
		assistantTool("edit", "edit_file", map[string]any{
			"path": "value.go", "old_text": "return 1", "new_text": "return 2", "expected_replacements": 1,
		}),
		assistantTool("test", "shell", map[string]any{"command": "go test ./...", "timeout": "2m"}),
		assistantTool("todos", "write_todos", map[string]any{"todos": []map[string]any{
			{"id": "fix", "content": "修复并验证", "status": "completed"},
		}}),
		assistantFinal("修复完成，测试通过。", provider.Usage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28}),
	}}
	agent, emitter, sink := newAgentTestHarness(t, root, fake, []policy.Rule{
		{Name: "allow-edit", Tool: "edit_file", Effects: []tools.Effect{tools.EffectWrite}, Decision: policy.DecisionAllow},
		{Name: "allow-tests", Tool: "shell", Effects: []tools.Effect{tools.EffectExecute}, CommandPrefixes: []string{"go test"}, Decision: policy.DecisionAllow},
	})
	result, err := agent.Run(context.Background(), RunRequest{
		RunID: "run-test", Task: "修复失败测试", Model: "fake:model", MaxTurns: 8, Security: "受控本地执行",
	}, emitter)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "value.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "return 2") || result.Summary != "修复完成，测试通过。" || len(result.Todos) != 1 || result.Todos[0].Status != tools.TodoCompleted {
		t.Fatalf("content=%q result=%+v", content, result)
	}
	assertEventKinds(t, sink.Events(), []events.Kind{
		events.KindRunStarted,
		events.KindMessageCompleted, events.KindToolProposed, events.KindToolStarted, events.KindToolCompleted,
		events.KindMessageCompleted, events.KindToolProposed, events.KindToolStarted, events.KindToolCompleted,
		events.KindMessageCompleted, events.KindToolProposed, events.KindToolStarted, events.KindToolCompleted,
		events.KindMessageCompleted, events.KindToolProposed, events.KindToolStarted, events.KindTodoUpdated, events.KindToolCompleted,
		events.KindMessageDelta, events.KindUsageUpdated, events.KindMessageCompleted, events.KindRunCompleted,
	})
	if len(fake.requests) != 5 || !lastToolResultContains(fake.requests[1], `"success":true`) || !strings.Contains(fake.requests[4].Messages[1].Content, "completed") {
		t.Fatalf("runtime did not preserve results/todos: requests=%+v", fake.requests)
	}
	if requestHasTool(fake.requests[0], tools.ReadContextSourceToolName) {
		t.Fatal("read_context_source was advertised before a rolling summary existed")
	}
}

func TestAgentReturnsPolicyDenialToModelWithoutSideEffect(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "value.go")
	if err := os.WriteFile(path, []byte("package fixture\nfunc Value() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &scriptedProvider{responses: []*provider.ChatResponse{
		assistantTool("edit", "edit_file", map[string]any{
			"path": "value.go", "old_text": "return 1", "new_text": "return 2", "expected_replacements": 1,
		}),
		assistantFinal("修改被策略拒绝，未改变文件。", provider.Usage{}),
	}}
	agent, emitter, _ := newAgentTestHarness(t, root, fake, nil)
	result, err := agent.Run(context.Background(), RunRequest{RunID: "run-test", Task: "修改", Model: "fake:model", MaxTurns: 4}, emitter)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeniedTools != 1 {
		t.Fatalf("DeniedTools=%d", result.DeniedTools)
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "return 2") {
		t.Fatalf("denied edit changed file: %s", content)
	}
	if len(fake.requests) != 2 || !lastToolResultContains(fake.requests[1], `"category":"denied"`) {
		t.Fatalf("denial was not returned to model: %+v", fake.requests)
	}
}

func TestAgentAcknowledgesVerifiedWriteRecoveryWithoutReexecution(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "value.txt")
	if err := os.WriteFile(path, []byte("written once\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &scriptedProvider{responses: []*provider.ChatResponse{
		assistantFinal("已根据核验结果继续。", provider.Usage{}),
	}}
	runtime, emitter, sink := newAgentTestHarness(t, root, fake, nil)
	result, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run-test", Task: "继续", Model: "fake:model", MaxTurns: 2,
		Recovery: &RecoveryAction{
			SourceRunID: "run-old", Kind: RecoveryVerifyWrite,
			Call: provider.ToolCall{
				ID: "write-1", Type: "function", Name: "write_file",
				Arguments: json.RawMessage(`{"path":"value.txt","content":"must not run","overwrite":true}`),
			},
		},
	}, emitter)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "已根据核验结果继续。" {
		t.Fatalf("result=%+v", result)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "written once\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	assertEventKinds(t, sink.Events(), []events.Kind{
		events.KindRunStarted, events.KindToolProposed, events.KindToolCompleted,
		events.KindRecoveryResolved, events.KindMessageDelta, events.KindMessageCompleted, events.KindRunCompleted,
	})
}

func TestAgentStopsRepeatedCallsFailuresAndMaxTurns(t *testing.T) {
	t.Run("same call", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
			t.Fatal(err)
		}
		fake := &scriptedProvider{responses: []*provider.ChatResponse{
			assistantTool("one", "read_file", map[string]any{"path": "a.txt"}),
			assistantTool("two", "read_file", map[string]any{"path": "a.txt"}),
			assistantTool("three", "read_file", map[string]any{"path": "a.txt"}),
		}}
		runtime, emitter, _ := newAgentTestHarness(t, root, fake, nil)
		_, err := runtime.Run(context.Background(), RunRequest{RunID: "run-test", Task: "读取", Model: "fake:model", MaxTurns: 5}, emitter)
		if !errors.Is(err, ErrRepeatedCall) {
			t.Fatalf("Run() error=%v", err)
		}
	})

	t.Run("same failure", func(t *testing.T) {
		root := t.TempDir()
		fake := &scriptedProvider{responses: []*provider.ChatResponse{
			assistantTool("one", "missing_tool", map[string]any{"attempt": 1}),
			assistantTool("two", "missing_tool", map[string]any{"attempt": 2}),
			assistantTool("three", "missing_tool", map[string]any{"attempt": 3}),
		}}
		runtime, emitter, _ := newAgentTestHarness(t, root, fake, nil)
		_, err := runtime.Run(context.Background(), RunRequest{RunID: "run-test", Task: "调用", Model: "fake:model", MaxTurns: 5}, emitter)
		if !errors.Is(err, ErrRepeatedFailure) {
			t.Fatalf("Run() error=%v", err)
		}
	})

	t.Run("max turns", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
			t.Fatal(err)
		}
		fake := &scriptedProvider{responses: []*provider.ChatResponse{
			assistantTool("one", "read_file", map[string]any{"path": "a.txt"}),
		}}
		runtime, emitter, _ := newAgentTestHarness(t, root, fake, nil)
		_, err := runtime.Run(context.Background(), RunRequest{RunID: "run-test", Task: "读取", Model: "fake:model", MaxTurns: 1}, emitter)
		if !errors.Is(err, ErrMaxTurns) {
			t.Fatalf("Run() error=%v", err)
		}
	})
}

func TestAgentEmitsCancelledTerminalEvent(t *testing.T) {
	root := t.TempDir()
	fake := &scriptedProvider{}
	runtime, emitter, sink := newAgentTestHarness(t, root, fake, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runtime.Run(ctx, RunRequest{RunID: "run-test", Task: "任务", Model: "fake:model", MaxTurns: 2}, emitter)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error=%v", err)
	}
	assertEventKinds(t, sink.Events(), []events.Kind{events.KindRunStarted, events.KindRunCancelled})
}

func TestAgentPersistsRecoverableContextBeforeNextModelCall(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "value.txt"), []byte("recoverable read value"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &scriptedProvider{responses: []*provider.ChatResponse{
		assistantTool("read", "read_file", map[string]any{"path": "value.txt"}),
		assistantTool("edit", "edit_file", map[string]any{
			"path": "value.txt", "old_text": "recoverable read value", "new_text": "changed value", "expected_replacements": 1,
		}),
		assistantFinal("恢复链完整。", provider.Usage{}),
	}}
	recorder := &memoryContextRecorder{}
	runtime, emitter, _ := newAgentTestHarnessWithRecorder(t, root, fake, []policy.Rule{{
		Name: "allow-edit", Tool: "edit_file", Effects: []tools.Effect{tools.EffectWrite}, Decision: policy.DecisionAllow,
	}}, recorder)
	history := []provider.Message{{Role: provider.RoleUser, Content: "旧任务"}, {Role: provider.RoleAssistant, Content: "旧回答"}}
	_, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run-test", Task: "继续", Model: "fake:model", MaxTurns: 4, History: history,
	}, emitter)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 3 || len(fake.requests[0].Messages) < 3 {
		t.Fatalf("provider requests=%+v", fake.requests)
	}
	providerMessages := fake.requests[0].Messages
	if providerMessages[len(providerMessages)-3].Content != "旧任务" || providerMessages[len(providerMessages)-2].Content != "旧回答" || providerMessages[len(providerMessages)-1].Content != "继续" {
		t.Fatalf("history was not restored in order: %+v", providerMessages)
	}
	if len(recorder.records) != 6 {
		t.Fatalf("context records=%d want=6: %+v", len(recorder.records), recorder.records)
	}
	if recorder.records[0].message.Role != provider.RoleUser || recorder.records[0].message.Content != "继续" || !recorder.records[0].complete {
		t.Fatalf("new user boundary=%+v", recorder.records[0])
	}
	if recorder.records[2].message.Role != provider.RoleTool || !recorder.records[2].complete || !strings.Contains(recorder.records[2].message.Content, "recoverable read value") {
		t.Fatalf("read result must be complete: %+v", recorder.records[2])
	}
	if recorder.records[4].message.Role != provider.RoleTool || recorder.records[4].complete || !strings.Contains(recorder.records[4].message.Content, `"recovery":"sanitized"`) {
		t.Fatalf("write result must be sanitized: %+v", recorder.records[4])
	}
	if strings.Contains(recorder.records[4].message.Content, "changed value") {
		t.Fatalf("sanitized write result leaked original output: %s", recorder.records[4].message.Content)
	}
}

func TestAgentStopsBeforeModelWhenContextPersistenceFails(t *testing.T) {
	root := t.TempDir()
	fake := &scriptedProvider{responses: []*provider.ChatResponse{assistantFinal("不应调用", provider.Usage{})}}
	recorder := &memoryContextRecorder{errAt: 1}
	runtime, emitter, sink := newAgentTestHarnessWithRecorder(t, root, fake, nil, recorder)
	_, err := runtime.Run(context.Background(), RunRequest{RunID: "run-test", Task: "任务", Model: "fake:model", MaxTurns: 2}, emitter)
	if err == nil || !strings.Contains(err.Error(), "persist private user context") {
		t.Fatalf("Run() error=%v", err)
	}
	if len(fake.requests) != 0 {
		t.Fatalf("provider was called after context failure: %+v", fake.requests)
	}
	assertEventKinds(t, sink.Events(), []events.Kind{events.KindRunStarted, events.KindRunFailed})
}

func TestAgentCompactsContextBeforeMainModelCall(t *testing.T) {
	root := t.TempDir()
	history := []provider.Message{{Role: provider.RoleUser, Content: "原始任务：保留兼容性和安全约束"}}
	for index := 0; index < 16; index++ {
		history = append(history, provider.Message{
			Role: provider.RoleAssistant, Content: fmt.Sprintf("历史片段-%d-%s", index, strings.Repeat("证据", 300)),
		})
	}
	fake := &scriptedProvider{responses: []*provider.ChatResponse{
		{Model: "fake:model", Message: provider.Message{Role: provider.RoleAssistant, Content: validAgentSummary()}, Usage: provider.Usage{InputTokens: 900, OutputTokens: 80}},
		assistantFinal("压缩后继续完成。", provider.Usage{InputTokens: 1200, OutputTokens: 20}),
	}}
	recorder := &memoryContextRecorder{position: len(history)}
	runtime, emitter, sink := newAgentTestHarnessWithRecorderAndBudget(t, root, fake, nil, recorder, 9000)
	_, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run-test", Task: "当前任务：继续验证", Model: "fake:model", MaxTurns: 2, History: history,
		Todos: []tools.Todo{{ID: "verify", Content: "保留未完成验收", Status: tools.TodoInProgress}},
	}, emitter)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 2 || len(fake.requests[0].Tools) != 0 || fake.requests[0].ToolChoice != "" || fake.requests[1].ToolChoice != provider.ToolChoiceAuto {
		t.Fatalf("requests=%+v", fake.requests)
	}
	if !requestHasTool(fake.requests[1], tools.ReadContextSourceToolName) {
		t.Fatal("compacted main request did not advertise read_context_source")
	}
	if len(recorder.compactions) != 1 || recorder.compactions[0].GeneratorVersion != agentcontext.SummaryProtocolVersion {
		t.Fatalf("compactions=%+v", recorder.compactions)
	}
	compaction := recorder.compactions[0]
	if compaction.EstimatedAfterUpperBound >= compaction.EstimatedBefore || compaction.EstimatedAfterUpperBound > 9000 {
		t.Fatalf("compaction budget=%+v", compaction)
	}
	t.Logf("estimated tokens before=%d after_upper_bound=%d", compaction.EstimatedBefore, compaction.EstimatedAfterUpperBound)
	mainMessages := fake.requests[1].Messages
	joined := ""
	for _, message := range mainMessages {
		joined += message.Content
	}
	if !strings.Contains(joined, "原始任务") || !strings.Contains(joined, "当前任务") ||
		!strings.Contains(joined, "不是原始事实证据") || !strings.Contains(joined, "保留未完成验收") ||
		strings.Contains(joined, "历史片段-0-") {
		t.Fatalf("main request did not preserve the expected boundaries")
	}
	kinds := make([]events.Kind, 0)
	for _, event := range sink.Events() {
		kinds = append(kinds, event.Kind)
	}
	if countKind(kinds, events.KindContextCompacted) != 1 || countKind(kinds, events.KindMessageCompleted) != 1 {
		t.Fatalf("event kinds=%v", kinds)
	}
}

func TestAgentFailsClosedWhenSummaryProviderFails(t *testing.T) {
	root := t.TempDir()
	history := []provider.Message{{Role: provider.RoleUser, Content: "原始任务"}}
	for index := 0; index < 16; index++ {
		history = append(history, provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("历史证据", 150)})
	}
	fake := &scriptedProvider{errors: []error{errors.New("summary unavailable")}}
	recorder := &memoryContextRecorder{position: len(history)}
	runtime, emitter, sink := newAgentTestHarnessWithRecorderAndBudget(t, root, fake, nil, recorder, 9000)
	_, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run-test", Task: "当前任务", Model: "fake:model", MaxTurns: 2, History: history,
	}, emitter)
	if err == nil || !strings.Contains(err.Error(), "generate context summary") {
		t.Fatalf("Run() error=%v", err)
	}
	if len(fake.requests) != 1 || len(recorder.compactions) != 0 {
		t.Fatalf("requests=%d compactions=%+v", len(fake.requests), recorder.compactions)
	}
	if eventsFound := sink.Events(); eventsFound[len(eventsFound)-1].Kind != events.KindRunFailed {
		t.Fatalf("events=%+v", eventsFound)
	}
}

func TestAgentCancellationDuringSummaryDoesNotPersistCompaction(t *testing.T) {
	root := t.TempDir()
	history := []provider.Message{{Role: provider.RoleUser, Content: "原始任务"}}
	for index := 0; index < 16; index++ {
		history = append(history, provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("历史证据", 150)})
	}
	fake := &scriptedProvider{errors: []error{context.Canceled}}
	recorder := &memoryContextRecorder{position: len(history)}
	runtime, emitter, sink := newAgentTestHarnessWithRecorderAndBudget(t, root, fake, nil, recorder, 9000)
	_, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run-test", Task: "当前任务", Model: "fake:model", MaxTurns: 2, History: history,
	}, emitter)
	if !errors.Is(err, context.Canceled) || len(recorder.compactions) != 0 {
		t.Fatalf("error=%v compactions=%+v", err, recorder.compactions)
	}
	eventsFound := sink.Events()
	if eventsFound[len(eventsFound)-1].Kind != events.KindRunCancelled {
		t.Fatalf("events=%+v", eventsFound)
	}
}

func TestAgentRecoveryRepreparesAndReauthorizesBeforeProviderCall(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "value.txt"), []byte("current value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	call := provider.ToolCall{ID: "recovery-read", Type: "function", Name: "read_file", Arguments: json.RawMessage(`{"path":"value.txt"}`)}
	fake := &scriptedProvider{responses: []*provider.ChatResponse{assistantFinal("恢复完成。", provider.Usage{})}}
	recorder := &memoryContextRecorder{}
	runtime, emitter, sink := newInteractiveRecoveryHarness(t, root, fake, "y\n", recorder)
	_, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run-test", Task: "继续处理", Model: "fake:model", MaxTurns: 2,
		History:  []provider.Message{{Role: provider.RoleUser, Content: "旧任务"}, {Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}}},
		Recovery: &RecoveryAction{SourceRunID: "run-old", Call: call, Kind: RecoveryRetryRead},
	}, emitter)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("provider requests=%d", len(fake.requests))
	}
	request := fake.requests[0]
	if len(request.Messages) < 3 {
		t.Fatalf("provider messages=%+v", request.Messages)
	}
	last := request.Messages[len(request.Messages)-3:]
	if last[0].Role != provider.RoleAssistant || last[0].ToolCalls[0].ID != call.ID ||
		last[1].Role != provider.RoleTool || last[1].ToolCallID != call.ID ||
		last[2].Role != provider.RoleUser || last[2].Content != "继续处理" {
		t.Fatalf("provider recovery history=%+v", last)
	}
	if len(recorder.records) < 2 || recorder.records[0].message.Role != provider.RoleTool || recorder.records[1].message.Role != provider.RoleUser {
		t.Fatalf("context records=%+v", recorder.records)
	}
	assertEventKinds(t, sink.Events(), []events.Kind{
		events.KindRunStarted, events.KindToolProposed, events.KindApprovalNeeded, events.KindApprovalResolved,
		events.KindToolStarted, events.KindToolCompleted, events.KindRecoveryResolved, events.KindMessageDelta, events.KindMessageCompleted, events.KindRunCompleted,
	})
}

func TestAgentRecoveryRejectsWriteWithoutSideEffect(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "value.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	call := provider.ToolCall{ID: "recovery-write", Type: "function", Name: "edit_file", Arguments: json.RawMessage(`{"path":"value.txt","old_text":"old","new_text":"new","expected_replacements":1}`)}
	fake := &scriptedProvider{responses: []*provider.ChatResponse{assistantFinal("已拒绝修改。", provider.Usage{})}}
	runtime, emitter, sink := newInteractiveRecoveryHarness(t, root, fake, "n\n", nil)
	_, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run-test", Task: "继续处理", Model: "fake:model", MaxTurns: 2,
		History:  []provider.Message{{Role: provider.RoleUser, Content: "旧任务"}, {Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}}},
		Recovery: &RecoveryAction{SourceRunID: "run-old", Call: call, Kind: RecoveryReapprove},
	}, emitter)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "old\n" {
		t.Fatalf("file content=%q error=%v", content, err)
	}
	for _, event := range sink.Events() {
		if event.Kind == events.KindToolStarted {
			t.Fatalf("rejected write emitted tool.started")
		}
	}
}

func newAgentTestHarness(t *testing.T, root string, fake provider.Provider, rules []policy.Rule) (*Agent, *events.Emitter, *events.MemorySink) {
	return newAgentTestHarnessWithRecorder(t, root, fake, rules, nil)
}

func newAgentTestHarnessWithRecorder(t *testing.T, root string, fake provider.Provider, rules []policy.Rule, recorder ContextRecorder) (*Agent, *events.Emitter, *events.MemorySink) {
	return newAgentTestHarnessWithRecorderAndBudget(t, root, fake, rules, recorder, 64_000)
}

func newAgentTestHarnessWithRecorderAndBudget(t *testing.T, root string, fake provider.Provider, rules []policy.Rule, recorder ContextRecorder, maxContextTokens int) (*Agent, *events.Emitter, *events.MemorySink) {
	t.Helper()
	guard, err := platform.NewPathGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	contextSourceTool, err := tools.NewReadContextSource(unavailableContextSourceReader{})
	if err != nil {
		t.Fatal(err)
	}
	registeredTools := append(tools.DefaultTools(), contextSourceTool)
	registry, err := tools.NewRegistry(registeredTools...)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := policy.NewEngine(policy.Options{Root: root, Mode: policy.ModeHeadless, CLI: rules})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now
	runtime, err := New(Options{
		Provider: fake, Registry: registry, Guard: guard, Policy: engine,
		Now: now, MaxContextTokens: maxContextTokens, ContextRecorder: recorder,
		MutationJournal: &memoryMutationJournal{intents: make(map[string]tools.MutationIntent)},
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := &events.MemorySink{}
	emitter, err := events.NewEmitter("run-test", sink, now)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, emitter, sink
}

type unavailableContextSourceReader struct{}

func (unavailableContextSourceReader) Describe(context.Context) (tools.ContextSourceDescriptor, error) {
	return tools.ContextSourceDescriptor{}, errors.New("summary unavailable in unit harness")
}

func (unavailableContextSourceReader) Load(context.Context, tools.ContextSourceDescriptor) ([]tools.ContextSourceMessage, error) {
	return nil, errors.New("summary unavailable in unit harness")
}

func requestHasTool(request provider.ChatRequest, name string) bool {
	for _, tool := range request.Tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}

func countKind(kinds []events.Kind, want events.Kind) int {
	count := 0
	for _, kind := range kinds {
		if kind == want {
			count++
		}
	}
	return count
}

func validAgentSummary() string {
	return `{"objective_and_constraints":["保留兼容和安全约束"],"decisions":[],"verified_evidence":["历史测试通过"],"unresolved_errors":["仍需验证"],"todo_approval_tool_state":["验收进行中"],"continuation_pointers":["继续运行测试"]}`
}

func newInteractiveRecoveryHarness(t *testing.T, root string, fake provider.Provider, input string, recorder ContextRecorder) (*Agent, *events.Emitter, *events.MemorySink) {
	t.Helper()
	guard, err := platform.NewPathGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry(tools.DefaultTools()...)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := policy.NewEngine(policy.Options{Root: root, Mode: policy.ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := policy.NewTextBroker(strings.NewReader(input), &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now
	runtime, err := New(Options{
		Provider: fake, Registry: registry, Guard: guard, Policy: engine, Broker: broker,
		Now: now, MaxContextTokens: 64_000, ContextRecorder: recorder,
		MutationJournal: &memoryMutationJournal{intents: make(map[string]tools.MutationIntent)},
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := &events.MemorySink{}
	emitter, err := events.NewEmitter("run-test", sink, now)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, emitter, sink
}

func assistantTool(id, name string, arguments any) *provider.ChatResponse {
	raw, _ := json.Marshal(arguments)
	return &provider.ChatResponse{Message: provider.Message{
		Role:      provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{ID: id, Type: "function", Name: name, Arguments: raw}},
	}}
}

func assistantFinal(content string, usage provider.Usage) *provider.ChatResponse {
	return &provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: content}, Usage: usage}
}

func writeAgentFixture(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"go.mod":        "module example.com/mengdie-runtime-fixture\n\ngo 1.26\n",
		"value.go":      "package fixture\n\nfunc Value() int { return 1 }\n",
		"value_test.go": "package fixture\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 2 { t.Fatal(\"want 2\") } }\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertEventKinds(t *testing.T, got []events.Event, want []events.Kind) {
	t.Helper()
	kinds := make([]events.Kind, len(got))
	for index, event := range got {
		kinds[index] = event.Kind
	}
	if len(kinds) != len(want) {
		t.Fatalf("event kinds=%v want=%v", kinds, want)
	}
	for index := range want {
		if kinds[index] != want[index] {
			t.Fatalf("event kinds=%v want=%v", kinds, want)
		}
	}
}

func lastToolResultContains(request provider.ChatRequest, value string) bool {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if request.Messages[index].Role == provider.RoleTool {
			return strings.Contains(request.Messages[index].Content, value)
		}
	}
	return false
}
