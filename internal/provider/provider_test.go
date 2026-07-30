// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestChatRequestValidate(t *testing.T) {
	strict := true
	request := ChatRequest{
		Model: "deepseek-chat",
		Messages: []Message{
			{Role: RoleUser, Content: "test"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Type: "function", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)}}},
			{Role: RoleTool, ToolCallID: "call-1", Content: "result"},
		},
		Tools:      []Tool{{Type: "function", Function: FunctionDefinition{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`), Strict: &strict}}},
		ToolChoice: ToolChoiceAuto,
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestChatRequestValidateRejectsInvalidContracts(t *testing.T) {
	nan := math.NaN()
	parallel := true
	call := ToolCall{ID: "call-1", Type: "function", Name: "read", Arguments: json.RawMessage(`{}`)}
	tool := Tool{Type: "function", Function: FunctionDefinition{Name: "read", Parameters: json.RawMessage(`{}`)}}
	tests := []struct {
		name    string
		request ChatRequest
	}{
		{"model", ChatRequest{Messages: []Message{{Role: RoleUser}}}},
		{"messages", ChatRequest{Model: "model"}},
		{"role", ChatRequest{Model: "model", Messages: []Message{{Role: "root"}}}},
		{"tool message", ChatRequest{Model: "model", Messages: []Message{{Role: RoleTool}}}},
		{"tool call id role", ChatRequest{Model: "model", Messages: []Message{{Role: RoleUser, ToolCallID: "call-1"}}}},
		{"tool calls role", ChatRequest{Model: "model", Messages: []Message{{Role: RoleUser, ToolCalls: []ToolCall{call}}}}},
		{"duplicate call id", ChatRequest{Model: "model", Messages: []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{call, call}}}}},
		{"tool type", ChatRequest{Model: "model", Messages: []Message{{Role: RoleUser}}, Tools: []Tool{{Type: "shell"}}}},
		{"tool schema", ChatRequest{Model: "model", Messages: []Message{{Role: RoleUser}}, Tools: []Tool{{Type: "function", Function: FunctionDefinition{Name: "test", Parameters: json.RawMessage(`{`)}}}}},
		{"duplicate tool name", ChatRequest{Model: "model", Messages: []Message{{Role: RoleUser}}, Tools: []Tool{tool, tool}}},
		{"tool choice", ChatRequest{Model: "model", Messages: []Message{{Role: RoleUser}}, ToolChoice: "sometimes"}},
		{"required without tools", ChatRequest{Model: "model", Messages: []Message{{Role: RoleUser}}, ToolChoice: ToolChoiceRequired}},
		{"parallel without tools", ChatRequest{Model: "model", Messages: []Message{{Role: RoleUser}}, ParallelToolCalls: &parallel}},
		{"temperature", ChatRequest{Model: "model", Messages: []Message{{Role: RoleUser}}, Temperature: &nan}},
		{"max tokens", ChatRequest{Model: "model", Messages: []Message{{Role: RoleUser}}, MaxTokens: -1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.request.Validate(); err == nil {
				t.Fatal("Validate() accepted invalid request")
			}
		})
	}
}

func TestErrorClassificationAndRedaction(t *testing.T) {
	cause := errors.New("connection reset")
	err := &Error{Category: ErrorNetwork, Retryable: true, RequestID: "req-1", Err: cause}
	if CategoryOf(err) != ErrorNetwork || !errors.Is(err, cause) {
		t.Fatalf("error classification failed: %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "network") || !strings.Contains(got, "req-1") {
		t.Fatalf("Error() = %q", got)
	}
	if _, ok := AsError(context.Canceled); ok {
		t.Fatal("AsError() classified a plain context error")
	}
}
