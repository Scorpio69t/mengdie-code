// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package openaicompat implements the deliberately small subset of the
// OpenAI-compatible chat-completions protocol used by MengDie Code.
package openaicompat

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

const (
	DefaultMaxEventBytes = 2 << 20
	DefaultMaxAttempts   = 3
	defaultTimeout       = 120 * time.Second
	maximumEventBytes    = 16 << 20
	maximumAttempts      = 5
)

type BackoffFunc func(failedAttempt int, retryAfter time.Duration) time.Duration
type SleepFunc func(context.Context, time.Duration) error

type Config struct {
	BaseURL        string
	APIKey         string
	HTTPClient     *http.Client
	Capabilities   provider.Capabilities
	RequestTimeout time.Duration
	MaxEventBytes  int
	MaxAttempts    int
	Backoff        BackoffFunc
	Sleep          SleepFunc
}

type Client struct {
	endpoint       string
	apiKey         string
	httpClient     *http.Client
	capabilities   provider.Capabilities
	requestTimeout time.Duration
	maxEventBytes  int
	maxAttempts    int
	backoff        BackoffFunc
	sleep          SleepFunc
}

func New(config Config) (*Client, error) {
	endpoint, err := completionEndpoint(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if config.RequestTimeout < 0 {
		return nil, errors.New("request timeout cannot be negative")
	}
	if config.MaxEventBytes < 0 || config.MaxEventBytes > maximumEventBytes {
		return nil, fmt.Errorf("max event bytes must be between 1 and %d", maximumEventBytes)
	}
	if config.MaxAttempts < 0 || config.MaxAttempts > maximumAttempts {
		return nil, fmt.Errorf("max attempts must be between 1 and %d", maximumAttempts)
	}

	requestTimeout := config.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultTimeout
	}
	maxEventBytes := config.MaxEventBytes
	if maxEventBytes == 0 {
		maxEventBytes = DefaultMaxEventBytes
	}
	maxAttempts := config.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = DefaultMaxAttempts
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	backoff := config.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}
	sleep := config.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	return &Client{
		endpoint:       endpoint,
		apiKey:         strings.TrimSpace(config.APIKey),
		httpClient:     httpClient,
		capabilities:   config.Capabilities,
		requestTimeout: requestTimeout,
		maxEventBytes:  maxEventBytes,
		maxAttempts:    maxAttempts,
		backoff:        backoff,
		sleep:          sleep,
	}, nil
}

func (c *Client) ID() string {
	return "openai-compatible"
}

func (c *Client) Capabilities(ctx context.Context, model string) (provider.Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return provider.Capabilities{}, contextProviderError(err)
	}
	if strings.TrimSpace(model) == "" {
		return provider.Capabilities{}, invalidRequestError("model_required", errors.New("model is required"))
	}
	return c.capabilities, nil
}

func (c *Client) Stream(ctx context.Context, request provider.ChatRequest, sink provider.StreamSink) (*provider.ChatResponse, error) {
	if sink == nil {
		return nil, invalidRequestError("sink_required", errors.New("stream sink is required"))
	}
	if err := request.Validate(); err != nil {
		return nil, invalidRequestError("invalid_chat_request", err)
	}
	if err := c.validateCapabilities(request); err != nil {
		return nil, err
	}
	body, err := marshalRequest(request)
	if err != nil {
		return nil, invalidRequestError("request_encoding_failed", err)
	}

	streamContext, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		response, visible, attemptErr := c.streamAttempt(streamContext, body, sink)
		if attemptErr == nil {
			return response, nil
		}
		providerErr, ok := provider.AsError(attemptErr)
		if !ok || !providerErr.Retryable || visible || attempt == c.maxAttempts {
			return nil, attemptErr
		}

		delay := c.backoff(attempt, providerErr.RetryAfter)
		if delay < 0 {
			delay = 0
		}
		retry := provider.StreamEvent{
			Kind: provider.StreamRetry,
			Retry: &provider.RetryInfo{
				Attempt:     attempt + 1,
				MaxAttempts: c.maxAttempts,
				Delay:       delay,
				Category:    providerErr.Category,
				StatusCode:  providerErr.StatusCode,
			},
		}
		if err := emitStream(streamContext, sink, retry); err != nil {
			return nil, classifyStreamError(err)
		}
		if err := c.sleep(streamContext, delay); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, contextProviderError(err)
			}
			return nil, &provider.Error{Category: provider.ErrorNetwork, Code: "retry_wait_failed", Err: err}
		}
	}
	return nil, &provider.Error{Category: provider.ErrorServer, Code: "retry_loop_exhausted"}
}

func (c *Client) streamAttempt(ctx context.Context, body []byte, sink provider.StreamSink) (result *provider.ChatResponse, visible bool, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, invalidRequestError("request_creation_failed", err)
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, false, classifyRequestError(ctx, err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			result = nil
			err = errors.Join(err, &provider.Error{
				Category: provider.ErrorNetwork,
				Code:     "response_close_failed",
				Err:      closeErr,
			})
		}
	}()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		drain(response.Body)
		return nil, false, classifyHTTPStatus(response)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "text/event-stream") {
		drain(response.Body)
		return nil, false, protocolError("unexpected_content_type", nil)
	}
	return decodeStream(ctx, response.Body, c.maxEventBytes, sink)
}

func (c *Client) validateCapabilities(request provider.ChatRequest) error {
	usesTools := len(request.Tools) > 0
	for _, message := range request.Messages {
		usesTools = usesTools || message.Role == provider.RoleTool || len(message.ToolCalls) > 0
	}
	if usesTools && !c.capabilities.ToolCalling {
		return invalidRequestError("tool_calling_unsupported", nil)
	}
	if request.ParallelToolCalls != nil && *request.ParallelToolCalls && !c.capabilities.ParallelTools {
		return invalidRequestError("parallel_tools_unsupported", nil)
	}
	if request.IncludeUsage && !c.capabilities.UsageInStream {
		return invalidRequestError("stream_usage_unsupported", nil)
	}
	for _, tool := range request.Tools {
		if tool.Function.Strict != nil && *tool.Function.Strict && !c.capabilities.StrictToolSchema {
			return invalidRequestError("strict_tool_schema_unsupported", nil)
		}
	}
	return nil
}

func completionEndpoint(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("base URL must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("base URL scheme must be http or https")
	}
	if parsed.User != nil {
		return "", errors.New("base URL must not contain user information")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("base URL must not contain a query or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/chat/completions"
	parsed.RawPath = ""
	return parsed.String(), nil
}

func classifyRequestError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return contextProviderError(ctxErr)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &provider.Error{Category: provider.ErrorTimeout, Code: "transport_timeout", Retryable: true, Err: err}
	}
	return &provider.Error{Category: provider.ErrorNetwork, Code: "transport_error", Retryable: true, Err: err}
}

func classifyHTTPStatus(response *http.Response) error {
	status := response.StatusCode
	classified := &provider.Error{
		StatusCode: status,
		Code:       "http_status_" + strconv.Itoa(status),
		RequestID:  requestID(response.Header),
		RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
	}
	switch status {
	case http.StatusUnauthorized:
		classified.Category = provider.ErrorAuthentication
	case http.StatusForbidden:
		classified.Category = provider.ErrorPermission
	case http.StatusRequestTimeout:
		classified.Category = provider.ErrorTimeout
		classified.Retryable = true
	case http.StatusTooManyRequests:
		classified.Category = provider.ErrorRateLimit
		classified.Retryable = true
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		classified.Category = provider.ErrorServer
		classified.Retryable = true
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusConflict, http.StatusUnprocessableEntity:
		classified.Category = provider.ErrorInvalidRequest
	default:
		if status >= 400 && status < 500 {
			classified.Category = provider.ErrorInvalidRequest
		} else if status >= 500 {
			classified.Category = provider.ErrorServer
		} else {
			classified.Category = provider.ErrorProtocol
		}
	}
	return classified
}

func requestID(header http.Header) string {
	for _, name := range []string{"x-request-id", "request-id"} {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseUint(value, 10, 31); err == nil {
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func defaultBackoff(failedAttempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	base := 200 * time.Millisecond * time.Duration(1<<min(failedAttempt-1, 4))
	var random [8]byte
	if _, err := cryptorand.Read(random[:]); err != nil {
		return base
	}
	jitter := time.Duration(binary.LittleEndian.Uint64(random[:]) % uint64(base/2+1))
	return base + jitter
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func contextProviderError(err error) error {
	if errors.Is(err, context.Canceled) {
		return &provider.Error{Category: provider.ErrorCanceled, Code: "canceled", Err: err}
	}
	return &provider.Error{Category: provider.ErrorTimeout, Code: "deadline_exceeded", Err: err}
}

func invalidRequestError(code string, err error) error {
	return &provider.Error{Category: provider.ErrorInvalidRequest, Code: code, Err: err}
}

func drain(reader io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(reader, 64<<10))
}
