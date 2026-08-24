// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// LLM is the provider-driven Extractor implementation. It loads the recent
// event transcript through an EventReader, asks the configured provider to
// propose 0-5 candidate memories, then parses the JSON Lines reply. Every
// provider/parse failure is swallowed — the Extractor contract is "produce
// as many as you can", not "fail the Run when the LLM is unavailable". app.
// Runtime is the only path that writes to the store; this type only
// returns candidate Memory values with the Authority field pre-set.
type LLM struct {
	provider provider.Provider
	model    string
	reader   EventReader
}

// NewLLM wires the LLM extractor to a Provider, a model name, and an
// EventReader. The three are captured by value; passing a nil Provider or
// nil EventReader keeps the Task-3 placeholder contract alive — Extract
// returns (nil, nil) without doing any I/O so callers can register LLM
// before the full wiring (provider, sqlite reader) is assembled.
func NewLLM(provider provider.Provider, model string, reader EventReader) *LLM {
	return &LLM{provider: provider, model: model, reader: reader}
}

// llmSystemPrompt is the prompt spec §5 pins verbatim. Do not edit without
// re-running the eval fixtures in evals/coding/ — every drift changes the
// shape of accepted outputs.
const llmSystemPrompt = `你是一个 Agent 运行观察者。从给定的运行轨迹中提取 0-5 条候选记忆。
每条输出 JSON {claim, source_type ∈ {user_message, agent_message}, reason}。
claim 必须是项目事实或偏好，不要复述命令。`

// llmMaxEvents caps the transcript size sent to the provider. Twenty rows
// keeps the prompt comfortably under typical 8K context windows and bounds
// the cost of the end-of-run extraction call.
const llmMaxEvents = 20

// llmTimeout bounds the synchronous wait on provider.Stream. The call is
// best-effort; on timeout we return (nil, nil) just like any other failure.
const llmTimeout = 20 * time.Second

// Extract implements extractor.Extractor. It returns (nil, nil) when the
// reader yields no events, when the provider errors, or when the reply
// fails to parse — those are all soft failures per the contract above.
func (l *LLM) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error) {
	if l == nil || l.provider == nil || l.reader == nil {
		return nil, nil
	}
	events, err := l.reader.Events(ctx, sessionID, llmMaxEvents)
	if err != nil || len(events) == 0 {
		return nil, nil
	}
	raw, err := l.callProvider(ctx, events)
	if err != nil {
		return nil, nil
	}
	return parseLLMResponse(raw), nil
}

// callProvider sends the formatted transcript to the provider with a hard
// 20-second timeout. The streaming reply is reassembled into a single
// string via llmSink so parseLLMResponse can treat it as JSON Lines.
func (l *LLM) callProvider(ctx context.Context, events []session.EventRow) (string, error) {
	extCtx, cancel := context.WithTimeout(ctx, llmTimeout)
	defer cancel()

	transcript := formatTranscript(events)
	req := provider.ChatRequest{
		Model: l.model,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: llmSystemPrompt},
			{Role: provider.RoleUser, Content: transcript},
		},
		Temperature: ptrFloat(0),
		MaxTokens:   512,
	}
	var buf strings.Builder
	sink := &llmSink{buf: &buf}
	if _, err := l.provider.Stream(extCtx, req, sink); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ptrFloat returns a pointer to v. Provider.ChatRequest.Temperature is a
// *float64, and a literal 0 (the deterministic-extraction setting) is not
// addressable in Go, so we wrap the helper. Local to the extractor
// package — internal/agent/runtime.go defines its own equivalent.
func ptrFloat(v float64) *float64 { return &v }

// llmSink is a minimal StreamSink that concatenates text-delta events into
// a single buffer. Non-text events (usage, retries, finished) are ignored;
// parseLLMResponse tolerates partial / multi-event replies.
type llmSink struct{ buf *strings.Builder }

func (s *llmSink) OnEvent(_ context.Context, e provider.StreamEvent) error {
	if e.Kind == provider.StreamTextDelta {
		s.buf.WriteString(e.Text)
	}
	return nil
}

// formatTranscript renders the recent events as a stable, line-prefixed
// block. Each line carries the wall-clock time (HH:MM:SS), the event kind,
// the tool name (or empty), and the ok/fail status — enough context for
// the LLM to anchor a claim without seeing raw payloads.
func formatTranscript(events []session.EventRow) string {
	var b strings.Builder
	for i, e := range events {
		if i >= llmMaxEvents {
			break
		}
		status := "ok"
		if !e.Success {
			status = "fail"
		}
		fmt.Fprintf(&b, "- %s | %s | %s | %s\n", e.Timestamp.Format("15:04:05"), e.Kind, e.Name, status)
	}
	return b.String()
}

// apiKeyRe matches runs of 20+ alphanumerics (plus '-' and '_'). That's
// long enough to catch sk-/API-key-style secrets without false-positiving
// on ordinary English words. Matches are replaced before parseLLMResponse
// sees them so the claim text and any logging downstream stay clean.
var apiKeyRe = regexp.MustCompile(`[A-Za-z0-9_-]{20,}`)

// redact replaces likely secrets with the placeholder "[REDACTED]".
func redact(s string) string { return apiKeyRe.ReplaceAllString(s, "[REDACTED]") }

// parseLLMResponse walks the provider reply line by line and returns one
// memory.Memory per valid JSON candidate. Invalid lines (non-JSON, missing
// fields, claim outside the 8..200 char window, unknown source_type) are
// dropped silently — the spec is "produce as many as you can" and an
// isolated bad line must not poison the batch.
//
// Per-line contract:
//   - Trim whitespace.
//   - Skip lines that do not start with '{'.
//   - Decode {claim, source_type, reason}.
//   - Drop when len(claim) < 8 || len(claim) > 200.
//   - Drop when source_type ∉ {user_message, agent_message}.
//   - Otherwise emit memory.Memory{Claim, Authority: AuthorityInferred}.
//
// The bufio.Scanner buffer is capped at 1 MiB; the provider reply is well
// under that even for the 5-line × 200-char worst case the prompt allows.
func parseLLMResponse(raw string) []memory.Memory {
	if raw == "" {
		return nil
	}
	var out []memory.Memory
	sc := bufio.NewScanner(strings.NewReader(redact(raw)))
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var c struct {
			Claim      string `json:"claim"`
			SourceType string `json:"source_type"`
			Reason     string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			continue
		}
		if len(c.Claim) < 8 || len(c.Claim) > 200 {
			continue
		}
		if c.SourceType != string(memory.SourceTypeUserMessage) &&
			c.SourceType != string(memory.SourceTypeAgentMessage) {
			continue
		}
		out = append(out, memory.Memory{
			Claim:     c.Claim,
			Authority: memory.AuthorityInferred,
		})
	}
	return out
}
