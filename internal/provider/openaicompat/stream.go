// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

type completionChunk struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *wireUsage    `json:"usage"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Content   string          `json:"content"`
	ToolCalls []toolCallDelta `json:"tool_calls"`
}

type toolCallDelta struct {
	Index    *int          `json:"index"`
	ID       string        `json:"id"`
	Type     string        `json:"type"`
	Function functionDelta `json:"function"`
}

type functionDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireUsage struct {
	PromptTokens         int64 `json:"prompt_tokens"`
	CompletionTokens     int64 `json:"completion_tokens"`
	TotalTokens          int64 `json:"total_tokens"`
	PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails  *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type callBuilder struct {
	id        strings.Builder
	name      strings.Builder
	arguments strings.Builder
	typeName  string
}

type streamAccumulator struct {
	response         provider.ChatResponse
	content          strings.Builder
	calls            map[int]*callBuilder
	assembledBytes   int
	maxResponseBytes int
	finishSeen       bool
	visible          bool
}

func decodeStream(
	ctx context.Context,
	reader io.Reader,
	maxEventBytes int,
	sink provider.StreamSink,
) (*provider.ChatResponse, bool, error) {
	if sink == nil {
		return nil, false, protocolError("sink_required", errors.New("stream sink is required"))
	}
	maxInt := int(^uint(0) >> 1)
	maxResponseBytes := maxInt
	if maxEventBytes <= maxInt/4 {
		maxResponseBytes = maxEventBytes * 4
	}
	accumulator := &streamAccumulator{
		calls:            make(map[int]*callBuilder),
		maxResponseBytes: maxResponseBytes,
	}
	done, err := parseSSE(ctx, reader, maxEventBytes, func(data []byte) error {
		return accumulator.consume(ctx, sink, data)
	})
	if err != nil {
		return nil, accumulator.visible, classifyStreamError(err)
	}
	response, err := accumulator.finalize(done)
	if err != nil {
		return nil, accumulator.visible, err
	}
	return response, accumulator.visible, nil
}

func (a *streamAccumulator) consume(ctx context.Context, sink provider.StreamSink, data []byte) error {
	var chunk completionChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return protocolError("invalid_chunk_json", err)
	}
	if chunk.ID != "" {
		if a.response.ID != "" && a.response.ID != chunk.ID {
			return protocolError("chunk_id_changed", nil)
		}
		a.response.ID = chunk.ID
	}
	if chunk.Model != "" {
		if a.response.Model != "" && a.response.Model != chunk.Model {
			return protocolError("chunk_model_changed", nil)
		}
		a.response.Model = chunk.Model
	}
	if len(chunk.Choices) > 1 {
		return protocolError("multiple_choices", nil)
	}
	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			return protocolError("unsupported_choice_index", nil)
		}
		if a.finishSeen && (choice.Delta.Content != "" || len(choice.Delta.ToolCalls) > 0 || choice.FinishReason != nil) {
			return protocolError("data_after_finish", nil)
		}
		if choice.Delta.Content != "" {
			if err := a.reserve(len(choice.Delta.Content)); err != nil {
				return err
			}
			a.visible = true
			a.content.WriteString(choice.Delta.Content)
			if err := emitStream(ctx, sink, provider.StreamEvent{Kind: provider.StreamTextDelta, Text: choice.Delta.Content}); err != nil {
				return err
			}
		}
		for _, delta := range choice.Delta.ToolCalls {
			if err := a.consumeToolDelta(ctx, sink, delta); err != nil {
				return err
			}
		}
		if choice.FinishReason != nil {
			if strings.TrimSpace(*choice.FinishReason) == "" {
				return protocolError("empty_finish_reason", nil)
			}
			a.finishSeen = true
			a.response.FinishReason = *choice.FinishReason
			if err := emitStream(ctx, sink, provider.StreamEvent{Kind: provider.StreamFinished, FinishReason: *choice.FinishReason}); err != nil {
				return err
			}
		}
	}
	if chunk.Usage != nil {
		if chunk.Usage.PromptTokens < 0 || chunk.Usage.CompletionTokens < 0 || chunk.Usage.TotalTokens < 0 || chunk.Usage.PromptCacheHitTokens < 0 ||
			(chunk.Usage.PromptTokensDetails != nil && chunk.Usage.PromptTokensDetails.CachedTokens < 0) {
			return protocolError("negative_usage", nil)
		}
		usage := normalizeUsage(*chunk.Usage)
		a.response.Usage = usage
		if err := emitStream(ctx, sink, provider.StreamEvent{Kind: provider.StreamUsage, Usage: &usage}); err != nil {
			return err
		}
	}
	return nil
}

func (a *streamAccumulator) consumeToolDelta(ctx context.Context, sink provider.StreamSink, delta toolCallDelta) error {
	if delta.Index == nil || *delta.Index < 0 {
		return protocolError("tool_call_index_missing", nil)
	}
	if delta.Type != "" && delta.Type != "function" {
		return protocolError("unsupported_tool_call_type", nil)
	}
	builder := a.calls[*delta.Index]
	if builder == nil {
		builder = &callBuilder{}
		a.calls[*delta.Index] = builder
	}
	if delta.Type != "" {
		builder.typeName = delta.Type
	}
	if err := a.reserve(len(delta.ID) + len(delta.Function.Name) + len(delta.Function.Arguments)); err != nil {
		return err
	}
	builder.id.WriteString(delta.ID)
	builder.name.WriteString(delta.Function.Name)
	builder.arguments.WriteString(delta.Function.Arguments)
	if delta.ID == "" && delta.Function.Name == "" && delta.Function.Arguments == "" {
		return nil
	}
	a.visible = true
	return emitStream(ctx, sink, provider.StreamEvent{
		Kind: provider.StreamToolCallDelta,
		ToolCall: &provider.ToolCallDelta{
			Index:             *delta.Index,
			IDFragment:        delta.ID,
			NameFragment:      delta.Function.Name,
			ArgumentsFragment: delta.Function.Arguments,
		},
	})
}

func (a *streamAccumulator) finalize(done bool) (*provider.ChatResponse, error) {
	if !a.finishSeen {
		if done {
			return nil, protocolError("done_before_finish", nil)
		}
		return nil, &provider.Error{
			Category:  provider.ErrorNetwork,
			Code:      "unexpected_eof",
			Retryable: true,
			Err:       io.ErrUnexpectedEOF,
		}
	}
	indexes := make([]int, 0, len(a.calls))
	for index := range a.calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	seenIDs := make(map[string]struct{}, len(indexes))
	for _, index := range indexes {
		builder := a.calls[index]
		call := provider.ToolCall{
			ID:        builder.id.String(),
			Type:      builder.typeName,
			Name:      builder.name.String(),
			Arguments: json.RawMessage(builder.arguments.String()),
		}
		if call.Type == "" {
			call.Type = "function"
		}
		if err := validateAssembledCall(call); err != nil {
			return nil, protocolError("invalid_tool_call", fmt.Errorf("index %d: %w", index, err))
		}
		if _, duplicate := seenIDs[call.ID]; duplicate {
			return nil, protocolError("duplicate_tool_call_id", nil)
		}
		seenIDs[call.ID] = struct{}{}
		a.response.Message.ToolCalls = append(a.response.Message.ToolCalls, call)
	}
	if len(a.response.Message.ToolCalls) > 0 && a.response.FinishReason != "tool_calls" {
		return nil, protocolError("tool_calls_without_finish_reason", nil)
	}
	if len(a.response.Message.ToolCalls) == 0 && a.response.FinishReason == "tool_calls" {
		return nil, protocolError("finish_reason_without_tool_calls", nil)
	}
	a.response.Message.Role = provider.RoleAssistant
	a.response.Message.Content = a.content.String()
	return &a.response, nil
}

func (a *streamAccumulator) reserve(additional int) error {
	if additional < 0 || a.assembledBytes > a.maxResponseBytes-additional {
		return protocolError("response_too_large", nil)
	}
	a.assembledBytes += additional
	return nil
}

func validateAssembledCall(call provider.ToolCall) error {
	request := provider.ChatRequest{
		Model:    "validation",
		Messages: []provider.Message{{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}}},
	}
	return request.Validate()
}

func normalizeUsage(usage wireUsage) provider.Usage {
	cacheRead := usage.PromptCacheHitTokens
	if cacheRead == 0 && usage.PromptTokensDetails != nil {
		cacheRead = usage.PromptTokensDetails.CachedTokens
	}
	return provider.Usage{
		InputTokens:     usage.PromptTokens,
		OutputTokens:    usage.CompletionTokens,
		TotalTokens:     usage.TotalTokens,
		CacheReadTokens: cacheRead,
	}
}

func emitStream(ctx context.Context, sink provider.StreamSink, event provider.StreamEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := sink.OnEvent(ctx, event); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return &provider.Error{Category: provider.ErrorSink, Err: err}
	}
	return nil
}

func classifyStreamError(err error) error {
	if _, ok := provider.AsError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return &provider.Error{Category: provider.ErrorCanceled, Err: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &provider.Error{Category: provider.ErrorTimeout, Err: err}
	case errors.Is(err, errSSEEventTooLarge):
		return protocolError("event_too_large", nil)
	default:
		return &provider.Error{Category: provider.ErrorNetwork, Retryable: true, Err: err}
	}
}

func protocolError(code string, err error) *provider.Error {
	return &provider.Error{Category: provider.ErrorProtocol, Code: code, Err: err}
}
