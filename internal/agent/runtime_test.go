// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package agent

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
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

type scriptedProvider struct {
	responses []*provider.ChatResponse
	errors    []error
	requests  []provider.ChatRequest
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

func newAgentTestHarness(t *testing.T, root string, fake provider.Provider, rules []policy.Rule) (*Agent, *events.Emitter, *events.MemorySink) {
	t.Helper()
	guard, err := platform.NewPathGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry(tools.DefaultTools()...)
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
		Now: now, MaxContextTokens: 64_000,
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
