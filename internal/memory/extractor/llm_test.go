// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// stubProvider is a deterministic Provider implementation that emits a
// single text delta with a pre-canned response and then finishes. It is the
// minimum needed to exercise provider.Provider.Stream without touching any
// real network code. The same shape is reused by every TestLLM* case below;
// each one swaps the response string and (optionally) the error flag.
type stubProvider struct {
	response string
	err      error
}

func (s *stubProvider) ID() string { return "stub" }

func (s *stubProvider) Capabilities(_ context.Context, _ string) (provider.Capabilities, error) {
	return provider.Capabilities{ToolCalling: true, MaxContextTokens: 4096}, nil
}

func (s *stubProvider) Stream(ctx context.Context, _ provider.ChatRequest, sink provider.StreamSink) (*provider.ChatResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	if sink != nil {
		_ = sink.OnEvent(ctx, provider.StreamEvent{Kind: provider.StreamTextDelta, Text: s.response})
	}
	return &provider.ChatResponse{
		Message: provider.Message{Role: provider.RoleAssistant, Content: s.response},
	}, nil
}

// TestLLMExtractsCandidatesFromStubProvider is the canonical happy-path
// test: a stub provider returns a single JSON Lines candidate and the
// fake reader yields one tool.completed event. The extractor must surface
// exactly one memory with AuthorityInferred and the claim text from the
// LLM's reply.
func TestLLMExtractsCandidatesFromStubProvider(t *testing.T) {
	stub := &stubProvider{response: `{"claim":"项目使用 stub 工具","source_type":"agent_message","reason":"inferred"}`}
	reader := &fakeEventReader{rows: []session.EventRow{{Kind: "tool.completed", Name: "edit_file", Success: true}}}
	l := NewLLM(stub, "stub-model", reader)
	got, err := l.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 memory, got %d", len(got))
	}
	if got[0].Claim != "项目使用 stub 工具" {
		t.Fatalf("claim: %q", got[0].Claim)
	}
	if got[0].Authority != memory.AuthorityInferred {
		t.Fatalf("authority: %s", got[0].Authority)
	}
}

// TestLLMExtractsMultipleCandidatesJSONL covers the JSON Lines fan-out:
// one provider reply carrying several newline-separated candidates yields
// one memory per valid line.
func TestLLMExtractsMultipleCandidatesJSONL(t *testing.T) {
	resp := strings.Join([]string{
		`{"claim":"偏好中文回复","source_type":"user_message","reason":"preference"}`,
		`{"claim":"运行 go fmt 之后才能提交","source_type":"agent_message","reason":"convention"}`,
		``, // blank line should be skipped
	}, "\n")
	stub := &stubProvider{response: resp}
	reader := &fakeEventReader{rows: []session.EventRow{{Kind: "tool.completed", Name: "shell", Success: true}}}
	l := NewLLM(stub, "stub-model", reader)
	got, err := l.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 memories, got %d (%+v)", len(got), got)
	}
	for _, mem := range got {
		if mem.Authority != memory.AuthorityInferred {
			t.Fatalf("expected AuthorityInferred, got %s", mem.Authority)
		}
	}
}

// TestLLMRedactsAPIKeysInOutput confirms the redact step rewrites long
// alphanumeric runs (≥20 chars) to "[REDACTED]" so provider replies never
// leak secrets into the extracted claim text or persisted rows.
func TestLLMRedactsAPIKeysInOutput(t *testing.T) {
	secret := "sk-aBcDeFgHiJkLmNoPqRsT1234"
	resp := `{"claim":"` + secret + `","source_type":"agent_message","reason":"x"}`
	stub := &stubProvider{response: resp}
	reader := &fakeEventReader{rows: []session.EventRow{{Kind: "tool.completed", Name: "edit_file", Success: true}}}
	l := NewLLM(stub, "stub-model", reader)
	got, err := l.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 memory, got %d", len(got))
	}
	if strings.Contains(got[0].Claim, secret) {
		t.Fatalf("expected secret redacted, got claim %q", got[0].Claim)
	}
	if !strings.Contains(got[0].Claim, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] placeholder, got claim %q", got[0].Claim)
	}
}

// TestLLMRejectsClaimsOutsideLengthWindow covers the length filter: claims
// shorter than 8 chars or longer than 200 chars are dropped silently. The
// long claim is constructed from "aaaa." segments so the redact regex
// cannot collapse it — a continuous 250-char alnum run would be rewritten
// to "[REDACTED]" (10 chars) and slip through the filter, masking the bug
// we're trying to catch.
func TestLLMRejectsClaimsOutsideLengthWindow(t *testing.T) {
	short := "abc"                    // 3 chars → too short
	long := strings.Repeat("a.", 150) // 300 chars → too long, '.' breaks the redact regex
	// Trim to a clean >200 length so the test mirrors what the parser sees.
	long = long[:251]
	resp := strings.Join([]string{
		`{"claim":"` + short + `","source_type":"agent_message","reason":"x"}`,
		`{"claim":"` + long + `","source_type":"agent_message","reason":"x"}`,
		`{"claim":"刚好在区间内","source_type":"agent_message","reason":"ok"}`,
	}, "\n")
	stub := &stubProvider{response: resp}
	reader := &fakeEventReader{rows: []session.EventRow{{Kind: "tool.completed", Name: "edit_file", Success: true}}}
	l := NewLLM(stub, "stub-model", reader)
	got, err := l.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 memory (only in-range claim survives), got %d (%+v)", len(got), got)
	}
	if got[0].Claim != "刚好在区间内" {
		t.Fatalf("unexpected surviving claim: %q", got[0].Claim)
	}
}

// TestLLMRejectsUnknownSourceType covers the source_type filter: only
// user_message and agent_message are accepted; everything else is dropped.
func TestLLMRejectsUnknownSourceType(t *testing.T) {
	resp := strings.Join([]string{
		`{"claim":"应当被接受","source_type":"agent_message","reason":"ok"}`,
		`{"claim":"应当被丢弃","source_type":"file","reason":"x"}`,
		`{"claim":"应当被丢弃","source_type":"command_result","reason":"x"}`,
	}, "\n")
	stub := &stubProvider{response: resp}
	reader := &fakeEventReader{rows: []session.EventRow{{Kind: "tool.completed", Name: "edit_file", Success: true}}}
	l := NewLLM(stub, "stub-model", reader)
	got, err := l.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 memory, got %d (%+v)", len(got), got)
	}
	if got[0].Claim != "应当被接受" {
		t.Fatalf("unexpected surviving claim: %q", got[0].Claim)
	}
}

// TestLLMSwallowsMalformedJSONLines confirms a line that fails json.Unmarshal
// does not break the rest of the batch: parse errors are dropped and the
// remaining valid lines still produce memories.
func TestLLMSwallowsMalformedJSONLines(t *testing.T) {
	resp := strings.Join([]string{
		`not-json`,
		`{"claim":"应当保留","source_type":"agent_message","reason":"ok"}`,
		`{"claim":`, // intentionally broken
	}, "\n")
	stub := &stubProvider{response: resp}
	reader := &fakeEventReader{rows: []session.EventRow{{Kind: "tool.completed", Name: "edit_file", Success: true}}}
	l := NewLLM(stub, "stub-model", reader)
	got, err := l.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 memory, got %d (%+v)", len(got), got)
	}
	if got[0].Claim != "应当保留" {
		t.Fatalf("unexpected surviving claim: %q", got[0].Claim)
	}
}

// TestLLMProviderErrorSwallowed documents the failure-degradation contract
// from spec §5: when provider.Stream returns an error, Extract must return
// (nil, nil) so app.Runtime never blocks a Run on memory extraction.
func TestLLMProviderErrorSwallowed(t *testing.T) {
	stub := &stubProvider{err: errStubProvider}
	reader := &fakeEventReader{rows: []session.EventRow{{Kind: "tool.completed", Name: "edit_file", Success: true}}}
	l := NewLLM(stub, "stub-model", reader)
	got, err := l.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Extract must swallow provider error, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil memories on provider error, got %+v", got)
	}
}

// TestLLMNoEventsReturnsNil documents the short-circuit when the session
// has no events to read: no LLM call is issued and Extract returns nil.
func TestLLMNoEventsReturnsNil(t *testing.T) {
	stub := &stubProvider{response: `{"claim":"不会被看到","source_type":"agent_message","reason":"x"}`}
	reader := &fakeEventReader{rows: nil}
	l := NewLLM(stub, "stub-model", reader)
	got, err := l.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil memories with no events, got %+v", got)
	}
}

// TestLLMNilProviderReturnsNil documents the defensive nil guard: a
// half-constructed *LLM (or one whose dependencies are nil) is a no-op,
// mirroring Rules.Extract.
func TestLLMNilProviderReturnsNil(t *testing.T) {
	l := NewLLM(nil, "stub-model", &fakeEventReader{})
	got, err := l.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil memories with nil provider, got %+v", got)
	}
}

// errStubProvider is a sentinel for "the stub provider pretended to fail"
// used by TestLLMProviderErrorSwallowed.
var errStubProvider = stubErr("stub provider failed")

type stubErr string

func (e stubErr) Error() string { return string(e) }
