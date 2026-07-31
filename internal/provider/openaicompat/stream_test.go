// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package openaicompat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

func TestDecodeStreamTextUsageAndUnknownReasoning(t *testing.T) {
	stream := sse(
		`{"id":"chat-1","model":"test-model","choices":[{"index":0,"delta":{"content":"你","reasoning_content":"secret"},"finish_reason":null}]}`,
		`{"id":"chat-1","model":"test-model","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":"stop"}]}`,
		`{"id":"chat-1","model":"test-model","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":4}}}`,
		"[DONE]",
	)
	var events []provider.StreamEvent
	response, visible, err := decodeStream(context.Background(), strings.NewReader(stream), 2<<20, provider.StreamSinkFunc(func(_ context.Context, event provider.StreamEvent) error {
		events = append(events, event)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !visible || response.Message.Content != "你好" || response.FinishReason != "stop" || response.Usage.CacheReadTokens != 4 {
		t.Fatalf("response=%+v visible=%v", response, visible)
	}
	if len(events) != 4 || events[0].Kind != provider.StreamTextDelta || events[2].Kind != provider.StreamFinished || events[3].Kind != provider.StreamUsage {
		t.Fatalf("events=%+v", events)
	}
	for _, event := range events {
		if strings.Contains(event.Text, "secret") {
			t.Fatal("reasoning content reached stream sink")
		}
	}
}

func TestDecodeStreamAssemblesToolCallFragments(t *testing.T) {
	stream := sse(
		`{"id":"chat-1","model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_","type":"function","function":{"name":"read_","arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
		`{"id":"chat-1","model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"1","function":{"name":"file","arguments":"\"README.md\"}"}}]},"finish_reason":"tool_calls"}]}`,
		"[DONE]",
	)
	response, visible, err := decodeStream(context.Background(), strings.NewReader(stream), 2<<20, provider.StreamSinkFunc(func(context.Context, provider.StreamEvent) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if !visible || len(response.Message.ToolCalls) != 1 {
		t.Fatalf("response=%+v visible=%v", response, visible)
	}
	call := response.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "read_file" || string(call.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("call=%+v", call)
	}
}

func TestDecodeStreamAssemblesMultipleToolCalls(t *testing.T) {
	stream := sse(
		`{"id":"chat-1","model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read_","arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
		`{"id":"chat-1","model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"write_","arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
		`{"id":"chat-1","model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"name":"file","arguments":"\"b.txt\"}"}},{"index":0,"function":{"name":"file","arguments":"\"a.txt\"}"}}]},"finish_reason":"tool_calls"}]}`,
		"[DONE]",
	)
	var events []provider.StreamEvent
	response, visible, err := decodeStream(context.Background(), strings.NewReader(stream), 2<<20, provider.StreamSinkFunc(func(_ context.Context, event provider.StreamEvent) error {
		events = append(events, event)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !visible || len(response.Message.ToolCalls) != 2 {
		t.Fatalf("response=%+v visible=%v", response, visible)
	}
	first, second := response.Message.ToolCalls[0], response.Message.ToolCalls[1]
	if first.ID != "call_a" || first.Name != "read_file" || string(first.Arguments) != `{"path":"a.txt"}` {
		t.Fatalf("first=%+v", first)
	}
	if second.ID != "call_b" || second.Name != "write_file" || string(second.Arguments) != `{"path":"b.txt"}` {
		t.Fatalf("second=%+v", second)
	}
	for _, event := range events {
		if event.Kind == provider.StreamFinished {
			continue
		}
		if event.ToolCall == nil || (event.ToolCall.Index != 0 && event.ToolCall.Index != 1) {
			t.Fatalf("unexpected event=%+v", event)
		}
	}
}

func TestDecodeStreamRejectsMalformedCompletion(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		code   string
	}{
		{"done before finish", sse("[DONE]"), "done_before_finish"},
		{"invalid json", sse(`{"choices":`), "invalid_chunk_json"},
		{"missing tool id", sse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"x","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, "[DONE]"), "invalid_tool_call"},
		{"invalid args", sse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"x","arguments":"{"}}]},"finish_reason":"tool_calls"}]}`, "[DONE]"), "invalid_tool_call"},
		{"missing tool index", sse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call","function":{"name":"x","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, "[DONE]"), "tool_call_index_missing"},
		{"tool finish mismatch", sse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"x","arguments":"{}"}}]},"finish_reason":"stop"}]}`, "[DONE]"), "tool_calls_without_finish_reason"},
		{"negative usage", sse(`{"choices":[],"usage":{"prompt_tokens":-1}}`, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, "[DONE]"), "negative_usage"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := decodeStream(context.Background(), strings.NewReader(test.stream), 2<<20, provider.StreamSinkFunc(func(context.Context, provider.StreamEvent) error { return nil }))
			providerErr, ok := provider.AsError(err)
			if !ok || providerErr.Category != provider.ErrorProtocol || providerErr.Code != test.code {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDecodeStreamClassifiesUnexpectedEOFAsRetryableNetwork(t *testing.T) {
	_, visible, err := decodeStream(context.Background(), strings.NewReader("data: {\"choices\":[]}\n\n"), 2<<20, provider.StreamSinkFunc(func(context.Context, provider.StreamEvent) error { return nil }))
	providerErr, ok := provider.AsError(err)
	if visible || !ok || providerErr.Category != provider.ErrorNetwork || providerErr.Code != "unexpected_eof" || !providerErr.Retryable {
		t.Fatalf("visible=%v error=%v", visible, err)
	}
}

func TestDecodeStreamBoundsCumulativeResponse(t *testing.T) {
	events := make([]string, 0, 12)
	for range 11 {
		events = append(events, `{"choices":[{"index":0,"delta":{"content":"`+strings.Repeat("x", 100)+`"},"finish_reason":null}]}`)
	}
	events = append(events, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	_, visible, err := decodeStream(context.Background(), strings.NewReader(sse(events...)), 256, provider.StreamSinkFunc(func(context.Context, provider.StreamEvent) error { return nil }))
	providerErr, ok := provider.AsError(err)
	if !visible || !ok || providerErr.Category != provider.ErrorProtocol || providerErr.Code != "response_too_large" {
		t.Fatalf("visible=%v error=%v", visible, err)
	}
}

func TestDecodeStreamPropagatesSinkErrorAndVisibility(t *testing.T) {
	want := errors.New("renderer failed")
	_, visible, err := decodeStream(context.Background(), strings.NewReader(sse(
		`{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`,
	)), 2<<20, provider.StreamSinkFunc(func(context.Context, provider.StreamEvent) error { return want }))
	providerErr, ok := provider.AsError(err)
	if !visible || !ok || providerErr.Category != provider.ErrorSink || !errors.Is(err, want) {
		t.Fatalf("visible=%v error=%v", visible, err)
	}
}

func sse(events ...string) string {
	var builder strings.Builder
	for _, event := range events {
		builder.WriteString("data: ")
		builder.WriteString(event)
		builder.WriteString("\n\n")
	}
	return builder.String()
}
