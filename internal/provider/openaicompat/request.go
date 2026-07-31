// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package openaicompat

import (
	"encoding/json"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

type wireRequest struct {
	Model             string              `json:"model"`
	Messages          []wireMessage       `json:"messages"`
	Tools             []wireTool          `json:"tools,omitempty"`
	ToolChoice        provider.ToolChoice `json:"tool_choice,omitempty"`
	Temperature       *float64            `json:"temperature,omitempty"`
	MaxTokens         int                 `json:"max_tokens,omitempty"`
	Stream            bool                `json:"stream"`
	StreamOptions     *wireStreamOptions  `json:"stream_options,omitempty"`
	ParallelToolCalls *bool               `json:"parallel_tool_calls,omitempty"`
}

type wireMessage struct {
	Role       provider.Role  `json:"role"`
	Content    string         `json:"content"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict,omitempty"`
}

type wireToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function wireCalledFunction `json:"function"`
}

type wireCalledFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func marshalRequest(request provider.ChatRequest) ([]byte, error) {
	wire := wireRequest{
		Model:             request.Model,
		ToolChoice:        request.ToolChoice,
		Temperature:       request.Temperature,
		MaxTokens:         request.MaxTokens,
		Stream:            true,
		ParallelToolCalls: request.ParallelToolCalls,
	}
	if request.IncludeUsage {
		wire.StreamOptions = &wireStreamOptions{IncludeUsage: true}
	}
	for _, message := range request.Messages {
		wireMessage := wireMessage{
			Role:       message.Role,
			Content:    message.Content,
			Name:       message.Name,
			ToolCallID: message.ToolCallID,
		}
		for _, call := range message.ToolCalls {
			wireMessage.ToolCalls = append(wireMessage.ToolCalls, wireToolCall{
				ID:   call.ID,
				Type: call.Type,
				Function: wireCalledFunction{
					Name:      call.Name,
					Arguments: string(call.Arguments),
				},
			})
		}
		wire.Messages = append(wire.Messages, wireMessage)
	}
	for _, tool := range request.Tools {
		parameters := tool.Function.Parameters
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{}`)
		}
		wire.Tools = append(wire.Tools, wireTool{
			Type: tool.Type,
			Function: wireFunction{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  parameters,
				Strict:      tool.Function.Strict,
			},
		})
	}
	return json.Marshal(wire)
}
