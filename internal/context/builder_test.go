// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package context

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

const validTestSummary = `{"objective_and_constraints":["保留约束"],"decisions":[],"verified_evidence":["测试通过"],"unresolved_errors":[],"todo_approval_tool_state":[],"continuation_pointers":["继续验证"]}`

func TestBuilderKeepsTaskTodosAndStableTools(t *testing.T) {
	builder, err := NewBuilder(Options{
		Model: "test-model", MaxContextTokens: 4096,
		Capabilities: provider.Capabilities{ToolCalling: true, ParallelTools: true, StrictToolSchema: true},
		Tools: []tools.ToolSpec{
			{Name: "z_tool", Description: "z", InputSchema: []byte(`{"type":"object"}`), Effects: []tools.Effect{tools.EffectRead}},
			{Name: "a_tool", Description: "a", InputSchema: []byte(`{"type":"object"}`), Effects: []tools.Effect{tools.EffectRead}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := builder.Build(State{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "修复测试"}},
		Todos:    []tools.Todo{{ID: "one", Content: "读取失败", Status: tools.TodoInProgress}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 3 || request.Messages[2].Content != "修复测试" || !strings.Contains(request.Messages[1].Content, "in_progress") {
		t.Fatalf("messages=%+v", request.Messages)
	}
	if len(request.Tools) != 2 || request.Tools[0].Function.Name != "a_tool" || request.Tools[1].Function.Name != "z_tool" {
		t.Fatalf("tools=%+v", request.Tools)
	}
	if request.ParallelToolCalls == nil || *request.ParallelToolCalls || request.Tools[0].Function.Strict == nil || !*request.Tools[0].Function.Strict {
		t.Fatalf("request capabilities not applied: %+v", request)
	}
}

func TestBuilderOmitsUnsupportedOptionalProviderFields(t *testing.T) {
	builder, err := NewBuilder(Options{
		Model: "test", MaxContextTokens: 1024,
		Capabilities: provider.Capabilities{ToolCalling: true},
		Tools:        []tools.ToolSpec{{Name: "read", InputSchema: []byte(`{}`), Effects: []tools.Effect{tools.EffectRead}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := builder.Build(State{Messages: []provider.Message{{Role: provider.RoleUser, Content: "read"}}})
	if err != nil {
		t.Fatal(err)
	}
	if request.ParallelToolCalls != nil || request.Tools[0].Function.Strict != nil || request.IncludeUsage {
		t.Fatalf("unsupported optional fields were enabled: %+v", request)
	}
}

func TestBuilderStopsInsteadOfDroppingRequiredContext(t *testing.T) {
	builder, err := NewBuilder(Options{
		Model: "test", MaxContextTokens: 32,
		Capabilities: provider.Capabilities{ToolCalling: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = builder.Build(State{Messages: []provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("任务", 200)}}})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Build() error=%v", err)
	}
}

func TestEstimateTextTokensDoesNotUndercountChineseAsASCII(t *testing.T) {
	if got := estimateTextTokens(strings.Repeat("梦", 100)); got != 100 {
		t.Fatalf("estimateTextTokens()=%d, want 100", got)
	}
	if got := estimateTextTokens(strings.Repeat("a", 100)); got != 25 {
		t.Fatalf("estimateTextTokens()=%d, want 25", got)
	}
}

func TestBuilderRequiresToolCapability(t *testing.T) {
	_, err := NewBuilder(Options{
		Model: "test", MaxContextTokens: 1024,
		Tools: []tools.ToolSpec{{Name: "read", InputSchema: []byte(`{}`), Effects: []tools.Effect{tools.EffectRead}}},
	})
	if !errors.Is(err, ErrToolCallingDisabled) {
		t.Fatalf("NewBuilder() error=%v", err)
	}
}

func TestBuilderPlansMiddleCompactionAndPreservesAnchors(t *testing.T) {
	builder, err := NewBuilder(Options{
		Model: "test", MaxContextTokens: 3000,
		Capabilities: provider.Capabilities{ToolCalling: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := []provider.Message{{Role: provider.RoleUser, Content: "原始任务：保持兼容并修复测试"}}
	for index := 0; index < 14; index++ {
		messages = append(messages, provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("历史证据", 70)})
	}
	messages[1] = provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
		ID: "call-1", Type: "function", Name: "shell", Arguments: json.RawMessage(`{"command":"go test ./..."}`),
	}}}
	messages[2] = provider.Message{
		Role: provider.RoleTool, ToolCallID: "call-1", Name: "shell",
		Content: "UNSAFE_PRIVATE_DETAIL-" + strings.Repeat("历史证据", 70),
	}
	messages = append(messages, provider.Message{Role: provider.RoleUser, Content: "当前任务：继续并保留安全约束"})
	sourceMessages := cloneMessages(messages)
	sourceMessages[2].Content = "恢复安全摘要"
	plan, err := builder.PlanCompaction(State{Messages: messages, SourceMessages: sourceMessages})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Source) == 0 || plan.Retained[0].Content != messages[0].Content {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.Retained[len(plan.Retained)-1].Content != messages[len(messages)-1].Content {
		t.Fatalf("latest task was not retained: %+v", plan.Retained)
	}
	request, err := builder.BuildSummaryRequest(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Tools) != 0 || request.ToolChoice != "" || request.MaxTokens != plan.MaxSummaryTokens {
		t.Fatalf("summary request=%+v", request)
	}
	if !strings.Contains(request.Messages[0].Content, "unresolved_errors") || !strings.Contains(request.Messages[1].Content, SummaryProtocolVersion) {
		t.Fatalf("summary prompts=%+v", request.Messages)
	}
	if !strings.Contains(request.Messages[1].Content, "恢复安全摘要") || strings.Contains(request.Messages[1].Content, "UNSAFE_PRIVATE_DETAIL") {
		t.Fatalf("summary source crossed the persistence boundary")
	}
	if _, err := builder.Build(State{Messages: plan.Retained, Summary: validTestSummary}); err != nil {
		t.Fatalf("compacted Build() error=%v", err)
	}
}

func TestBuilderSummaryStaysAfterOriginalTaskAnchor(t *testing.T) {
	builder, err := NewBuilder(Options{
		Model: "test", MaxContextTokens: 4096,
		Capabilities: provider.Capabilities{ToolCalling: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := builder.Build(State{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "原始任务"},
			{Role: provider.RoleAssistant, Content: "最近事实"},
		},
		Summary: validTestSummary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Messages[1].Content != "原始任务" || request.Messages[2].Role != provider.RoleDeveloper ||
		!strings.Contains(request.Messages[2].Content, "不是原始事实证据") ||
		!strings.Contains(request.Messages[2].Content, "read_context_source") || request.Messages[3].Content != "最近事实" {
		t.Fatalf("messages=%+v", request.Messages)
	}
}

func TestBuilderRefusesToCompactLatestTask(t *testing.T) {
	builder, err := NewBuilder(Options{
		Model: "test", MaxContextTokens: 300,
		Capabilities: provider.Capabilities{ToolCalling: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = builder.PlanCompaction(State{Messages: []provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("当前任务", 500)},
		{Role: provider.RoleAssistant, Content: "最近回答"},
	}})
	if !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("PlanCompaction() error=%v", err)
	}
}

func TestValidateSummaryRequiresEveryProtocolField(t *testing.T) {
	if err := ValidateSummary(validTestSummary); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`not json`,
		`{"objective_and_constraints":["目标"],"continuation_pointers":["继续"]}`,
		`{"objective_and_constraints":[],"decisions":[],"verified_evidence":[],"unresolved_errors":[],"todo_approval_tool_state":[],"continuation_pointers":["继续"]}`,
	} {
		if err := ValidateSummary(invalid); err == nil {
			t.Fatalf("ValidateSummary(%q) unexpectedly succeeded", invalid)
		}
	}
}
