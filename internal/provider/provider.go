// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package provider defines the smallest model-provider boundary required by
// the M1 agent runtime. It intentionally contains no terminal or tool runner
// dependencies.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Provider is a replaceable model boundary. Implementations must stop reading
// the upstream stream when the context or StreamSink returns an error.
type Provider interface {
	ID() string
	Capabilities(context.Context, string) (Capabilities, error)
	Stream(context.Context, ChatRequest, StreamSink) (*ChatResponse, error)
}

type Capabilities struct {
	ToolCalling      bool
	ParallelTools    bool
	UsageInStream    bool
	StrictToolSchema bool
	MaxContextTokens int
}

type Role string

const (
	RoleDeveloper Role = "developer"
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type Tool struct {
	Type     string
	Function FunctionDefinition
}

type FunctionDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Strict      *bool
}

type ToolCall struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolChoice string

const (
	ToolChoiceNone     ToolChoice = "none"
	ToolChoiceAuto     ToolChoice = "auto"
	ToolChoiceRequired ToolChoice = "required"
)

type ChatRequest struct {
	Model             string
	Messages          []Message
	Tools             []Tool
	ToolChoice        ToolChoice
	Temperature       *float64
	MaxTokens         int
	IncludeUsage      bool
	ParallelToolCalls *bool
}

func (r ChatRequest) Validate() error {
	if strings.TrimSpace(r.Model) == "" {
		return errors.New("chat request model is required")
	}
	if len(r.Messages) == 0 {
		return errors.New("chat request requires at least one message")
	}
	for i, message := range r.Messages {
		switch message.Role {
		case RoleDeveloper, RoleSystem, RoleUser, RoleAssistant, RoleTool:
		default:
			return fmt.Errorf("message %d has unsupported role %q", i, message.Role)
		}
		if message.Role == RoleTool && strings.TrimSpace(message.ToolCallID) == "" {
			return fmt.Errorf("tool message %d requires tool_call_id", i)
		}
		if message.Role != RoleTool && message.ToolCallID != "" {
			return fmt.Errorf("message %d has tool_call_id outside tool role", i)
		}
		if message.Role != RoleAssistant && len(message.ToolCalls) > 0 {
			return fmt.Errorf("message %d has tool calls outside assistant role", i)
		}
		seenCallIDs := make(map[string]struct{}, len(message.ToolCalls))
		for j, call := range message.ToolCalls {
			if err := validateToolCall(call); err != nil {
				return fmt.Errorf("message %d tool call %d: %w", i, j, err)
			}
			if _, duplicate := seenCallIDs[call.ID]; duplicate {
				return fmt.Errorf("message %d has duplicate tool call id", i)
			}
			seenCallIDs[call.ID] = struct{}{}
		}
	}
	seenToolNames := make(map[string]struct{}, len(r.Tools))
	for i, tool := range r.Tools {
		if tool.Type != "function" {
			return fmt.Errorf("tool %d type must be function", i)
		}
		if strings.TrimSpace(tool.Function.Name) == "" {
			return fmt.Errorf("tool %d function name is required", i)
		}
		if _, duplicate := seenToolNames[tool.Function.Name]; duplicate {
			return fmt.Errorf("tool %d duplicates function name", i)
		}
		seenToolNames[tool.Function.Name] = struct{}{}
		if len(tool.Function.Parameters) > 0 && !json.Valid(tool.Function.Parameters) {
			return fmt.Errorf("tool %d parameters must be valid JSON", i)
		}
	}
	switch r.ToolChoice {
	case "", ToolChoiceNone, ToolChoiceAuto, ToolChoiceRequired:
	default:
		return fmt.Errorf("unsupported tool choice %q", r.ToolChoice)
	}
	if r.ToolChoice == ToolChoiceRequired && len(r.Tools) == 0 {
		return errors.New("required tool choice needs at least one tool")
	}
	if r.ParallelToolCalls != nil && *r.ParallelToolCalls && len(r.Tools) == 0 {
		return errors.New("parallel tool calls need at least one tool")
	}
	if r.Temperature != nil && (math.IsNaN(*r.Temperature) || math.IsInf(*r.Temperature, 0)) {
		return errors.New("temperature must be finite")
	}
	if r.MaxTokens < 0 {
		return errors.New("max_tokens cannot be negative")
	}
	return nil
}

func validateToolCall(call ToolCall) error {
	if strings.TrimSpace(call.ID) == "" {
		return errors.New("id is required")
	}
	if call.Type != "function" {
		return errors.New("type must be function")
	}
	if strings.TrimSpace(call.Name) == "" {
		return errors.New("function name is required")
	}
	if len(call.Arguments) == 0 || !json.Valid(call.Arguments) {
		return errors.New("function arguments must be valid JSON")
	}
	return nil
}

type Usage struct {
	InputTokens     int64
	OutputTokens    int64
	TotalTokens     int64
	CacheReadTokens int64
}

type ChatResponse struct {
	ID           string
	Model        string
	Message      Message
	FinishReason string
	Usage        Usage
}

type StreamEventKind string

const (
	StreamTextDelta     StreamEventKind = "text.delta"
	StreamToolCallDelta StreamEventKind = "tool_call.delta"
	StreamUsage         StreamEventKind = "usage"
	StreamRetry         StreamEventKind = "retry"
	StreamFinished      StreamEventKind = "finished"
)

type ToolCallDelta struct {
	Index             int
	IDFragment        string
	NameFragment      string
	ArgumentsFragment string
}

type RetryInfo struct {
	Attempt     int
	MaxAttempts int
	Delay       time.Duration
	Category    ErrorCategory
	StatusCode  int
}

type StreamEvent struct {
	Kind         StreamEventKind
	Text         string
	ToolCall     *ToolCallDelta
	Usage        *Usage
	Retry        *RetryInfo
	FinishReason string
}

type StreamSink interface {
	OnEvent(context.Context, StreamEvent) error
}

type StreamSinkFunc func(context.Context, StreamEvent) error

func (f StreamSinkFunc) OnEvent(ctx context.Context, event StreamEvent) error {
	return f(ctx, event)
}
