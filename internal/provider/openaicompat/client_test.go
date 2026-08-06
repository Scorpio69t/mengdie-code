// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

func TestClientStreamsRequestAndMapsWireFields(t *testing.T) {
	strict := true
	parallel := true
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Errorf("authorization = %q", got)
		}
		if got := request.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("accept = %q", got)
		}
		if got := request.Header.Get("User-Agent"); got != defaultUserAgent {
			t.Errorf("user-agent = %q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode body: %v", err)
		}
		response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(response, sse(
			`{"id":"chat-1","model":"model","choices":[{"index":0,"delta":{"content":"完成"},"finish_reason":"stop"}]}`,
			`{"id":"chat-1","model":"model","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":1,"total_tokens":9}}`,
			"[DONE]",
		))
	}))
	defer server.Close()

	client := newTestClient(t, Config{
		BaseURL: server.URL + "/v1/",
		APIKey:  " test-secret ",
		Capabilities: provider.Capabilities{
			ToolCalling:      true,
			ParallelTools:    true,
			UsageInStream:    true,
			StrictToolSchema: true,
		},
	})
	var events []provider.StreamEvent
	result, err := client.Stream(context.Background(), provider.ChatRequest{
		Model: "model",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "修复测试"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-1", Type: "function", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)}}},
			{Role: provider.RoleTool, Content: "ok", ToolCallID: "call-1"},
		},
		Tools: []provider.Tool{{Type: "function", Function: provider.FunctionDefinition{
			Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`), Strict: &strict,
		}}},
		ToolChoice:        provider.ToolChoiceAuto,
		IncludeUsage:      true,
		ParallelToolCalls: &parallel,
	}, provider.StreamSinkFunc(func(_ context.Context, event provider.StreamEvent) error {
		events = append(events, event)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Message.Content != "完成" || result.Usage.TotalTokens != 9 || len(events) != 3 {
		t.Fatalf("result=%+v events=%+v", result, events)
	}
	if received["stream"] != true || received["tool_choice"] != "auto" || received["parallel_tool_calls"] != true {
		t.Fatalf("wire request=%+v", received)
	}
	streamOptions, ok := received["stream_options"].(map[string]any)
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("stream_options=%+v", received["stream_options"])
	}
	messages := received["messages"].([]any)
	assistant := messages[1].(map[string]any)
	toolCalls := assistant["tool_calls"].([]any)
	function := toolCalls[0].(map[string]any)["function"].(map[string]any)
	if function["arguments"] != `{"path":"README.md"}` {
		t.Fatalf("tool arguments=%q", function["arguments"])
	}
}

func TestClientRetriesRateLimitBeforeVisibleOutput(t *testing.T) {
	var attempts atomic.Int32
	var delays []time.Duration
	client := newTestClient(t, Config{
		BaseURL: "https://example.invalid/v1",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempt := attempts.Add(1)
			if attempt == 1 {
				return testResponse(http.StatusTooManyRequests, "application/json", "sensitive body", http.Header{"Retry-After": []string{"2"}}), nil
			}
			return testResponse(http.StatusOK, "text/event-stream", sse(`{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]"), nil), nil
		})},
		Capabilities: provider.Capabilities{},
		Backoff: func(_ int, retryAfter time.Duration) time.Duration {
			if retryAfter != 2*time.Second {
				t.Errorf("retry-after = %s", retryAfter)
			}
			return retryAfter
		},
		Sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	})
	var retries []provider.RetryInfo
	result, err := client.Stream(context.Background(), basicRequest(), provider.StreamSinkFunc(func(_ context.Context, event provider.StreamEvent) error {
		if event.Retry != nil {
			retries = append(retries, *event.Retry)
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || result.Message.Content != "ok" || len(delays) != 1 || len(retries) != 1 {
		t.Fatalf("attempts=%d result=%+v delays=%v retries=%+v", attempts.Load(), result, delays, retries)
	}
	if retries[0].Attempt != 2 || retries[0].MaxAttempts != DefaultMaxAttempts || retries[0].Category != provider.ErrorRateLimit {
		t.Fatalf("retry=%+v", retries[0])
	}
}

func TestClientRetriesUnexpectedEOFOnlyBeforeVisibleDelta(t *testing.T) {
	tests := []struct {
		name         string
		firstBody    string
		wantAttempts int32
		wantText     string
		wantErr      bool
	}{
		{"before visible", "data: {\"choices\":[]}\n\n", 2, "recovered", false},
		{"after visible", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n", 1, "partial", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			client := newTestClient(t, Config{
				BaseURL: "https://example.invalid/v1",
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					if attempts.Add(1) == 1 {
						return testResponse(http.StatusOK, "text/event-stream", test.firstBody, nil), nil
					}
					return testResponse(http.StatusOK, "text/event-stream", sse(`{"choices":[{"index":0,"delta":{"content":"recovered"},"finish_reason":"stop"}]}`, "[DONE]"), nil), nil
				})},
				Sleep: func(context.Context, time.Duration) error { return nil },
			})
			var text strings.Builder
			result, err := client.Stream(context.Background(), basicRequest(), provider.StreamSinkFunc(func(_ context.Context, event provider.StreamEvent) error {
				text.WriteString(event.Text)
				return nil
			}))
			if (err != nil) != test.wantErr || attempts.Load() != test.wantAttempts || text.String() != test.wantText {
				t.Fatalf("attempts=%d text=%q result=%+v error=%v", attempts.Load(), text.String(), result, err)
			}
		})
	}
}

func TestClientDoesNotRetryAuthenticationOrLeakBodiesAndCredentials(t *testing.T) {
	const secret = "credential-must-not-leak"
	var attempts atomic.Int32
	client := newTestClient(t, Config{
		BaseURL: "https://example.invalid/v1",
		APIKey:  secret,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			attempts.Add(1)
			if request.Header.Get("Authorization") != "Bearer "+secret {
				t.Error("missing authorization header")
			}
			return testResponse(http.StatusUnauthorized, "application/json", `{"error":"`+secret+` prompt text"}`, http.Header{"X-Request-Id": []string{"req-safe"}}), nil
		})},
	})
	_, err := client.Stream(context.Background(), basicRequest(), discardSink())
	providerErr, ok := provider.AsError(err)
	if attempts.Load() != 1 || !ok || providerErr.Category != provider.ErrorAuthentication || providerErr.RequestID != "req-safe" {
		t.Fatalf("attempts=%d error=%v", attempts.Load(), err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "prompt text") {
		t.Fatalf("error leaked sensitive data: %v", err)
	}
}

func TestClientStopsAtRetryLimit(t *testing.T) {
	var attempts atomic.Int32
	client := newTestClient(t, Config{
		BaseURL:     "https://example.invalid/v1",
		MaxAttempts: 2,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts.Add(1)
			return testResponse(http.StatusServiceUnavailable, "application/json", "unavailable", nil), nil
		})},
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	_, err := client.Stream(context.Background(), basicRequest(), discardSink())
	providerErr, ok := provider.AsError(err)
	if attempts.Load() != 2 || !ok || providerErr.Category != provider.ErrorServer || !providerErr.Retryable {
		t.Fatalf("attempts=%d error=%v", attempts.Load(), err)
	}
}

func TestClientCapabilityGatesBeforeNetwork(t *testing.T) {
	strict := true
	parallel := true
	tests := []struct {
		name         string
		capabilities provider.Capabilities
		request      provider.ChatRequest
		code         string
	}{
		{"tools", provider.Capabilities{}, withTool(basicRequest(), nil), "tool_calling_unsupported"},
		{"usage", provider.Capabilities{}, func() provider.ChatRequest { request := basicRequest(); request.IncludeUsage = true; return request }(), "stream_usage_unsupported"},
		{"parallel", provider.Capabilities{ToolCalling: true}, func() provider.ChatRequest {
			request := withTool(basicRequest(), nil)
			request.ParallelToolCalls = &parallel
			return request
		}(), "parallel_tools_unsupported"},
		{"strict", provider.Capabilities{ToolCalling: true}, withTool(basicRequest(), &strict), "strict_tool_schema_unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, Config{BaseURL: "https://example.invalid/v1", Capabilities: test.capabilities})
			_, err := client.Stream(context.Background(), test.request, discardSink())
			providerErr, ok := provider.AsError(err)
			if !ok || providerErr.Category != provider.ErrorInvalidRequest || providerErr.Code != test.code {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestClientRejectsProtocolErrorsWithoutRetry(t *testing.T) {
	var attempts atomic.Int32
	client := newTestClient(t, Config{
		BaseURL: "https://example.invalid/v1",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts.Add(1)
			return testResponse(http.StatusOK, "application/json", "{}", nil), nil
		})},
	})
	_, err := client.Stream(context.Background(), basicRequest(), discardSink())
	providerErr, ok := provider.AsError(err)
	if attempts.Load() != 1 || !ok || providerErr.Category != provider.ErrorProtocol || providerErr.Code != "unexpected_content_type" {
		t.Fatalf("attempts=%d error=%v", attempts.Load(), err)
	}
}

func TestClientCancellationAndSinkFailure(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		client := newTestClient(t, Config{
			BaseURL: "https://example.invalid/v1",
			HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				return nil, request.Context().Err()
			})},
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.Stream(ctx, basicRequest(), discardSink())
		if provider.CategoryOf(err) != provider.ErrorCanceled {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("sink failure closes body", func(t *testing.T) {
		body := &trackingBody{Reader: strings.NewReader(sse(`{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`))}
		client := newTestClient(t, Config{
			BaseURL: "https://example.invalid/v1",
			HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
			})},
		})
		want := errors.New("sink failed")
		_, err := client.Stream(context.Background(), basicRequest(), provider.StreamSinkFunc(func(context.Context, provider.StreamEvent) error { return want }))
		if provider.CategoryOf(err) != provider.ErrorSink || !errors.Is(err, want) || !body.closed.Load() {
			t.Fatalf("closed=%v error=%v", body.closed.Load(), err)
		}
	})
}

func TestClientReportsResponseCloseFailure(t *testing.T) {
	want := errors.New("response close failed")
	body := &trackingBody{
		Reader: strings.NewReader(sse(
			`{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
			"[DONE]",
		)),
		closeErr: want,
	}
	client := newTestClient(t, Config{
		BaseURL:     "https://example.invalid/v1",
		MaxAttempts: 1,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       body,
			}, nil
		})},
	})

	response, err := client.Stream(context.Background(), basicRequest(), discardSink())
	providerErr, ok := provider.AsError(err)
	if response != nil || !ok || providerErr.Code != "response_close_failed" || !errors.Is(err, want) || !body.closed.Load() {
		t.Fatalf("response=%v closed=%v error=%v", response, body.closed.Load(), err)
	}
}

func TestClientConfigurationAndCapabilities(t *testing.T) {
	invalid := []Config{
		{},
		{BaseURL: "ftp://example.com/v1"},
		{BaseURL: "https://user:pass@example.com/v1"},
		{BaseURL: "https://example.com/v1?token=bad"},
		{BaseURL: "https://example.com/v1", MaxAttempts: maximumAttempts + 1},
		{BaseURL: "https://example.com/v1", MaxEventBytes: maximumEventBytes + 1},
	}
	for _, config := range invalid {
		if _, err := New(config); err == nil {
			t.Fatalf("New(%+v) succeeded", config)
		}
	}
	caps := provider.Capabilities{ToolCalling: true, MaxContextTokens: 64_000}
	client := newTestClient(t, Config{BaseURL: "https://example.com/v1", Capabilities: caps})
	got, err := client.Capabilities(context.Background(), "model")
	if err != nil || got != caps || client.ID() != "openai-compatible" {
		t.Fatalf("id=%q caps=%+v error=%v", client.ID(), got, err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("3", now); got != 3*time.Second {
		t.Fatalf("seconds=%s", got)
	}
	if got := parseRetryAfter(now.Add(5*time.Second).Format(http.TimeFormat), now); got != 5*time.Second {
		t.Fatalf("date=%s", got)
	}
}

func newTestClient(t *testing.T, config Config) *Client {
	t.Helper()
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 5 * time.Second
	}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func basicRequest() provider.ChatRequest {
	return provider.ChatRequest{Model: "model", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}}
}

func withTool(request provider.ChatRequest, strict *bool) provider.ChatRequest {
	request.Tools = []provider.Tool{{Type: "function", Function: provider.FunctionDefinition{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`), Strict: strict}}}
	return request
}

func discardSink() provider.StreamSink {
	return provider.StreamSinkFunc(func(context.Context, provider.StreamEvent) error { return nil })
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testResponse(status int, contentType, body string, extra http.Header) *http.Response {
	header := make(http.Header)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	for name, values := range extra {
		header[name] = append([]string(nil), values...)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

type trackingBody struct {
	io.Reader
	closed   atomic.Bool
	closeErr error
}

func (body *trackingBody) Close() error {
	body.closed.Store(true)
	return body.closeErr
}
