// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package context

import (
	"errors"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

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
