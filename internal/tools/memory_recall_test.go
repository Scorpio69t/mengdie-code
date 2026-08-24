// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

// stubMemoryRecallRetriever implements MemoryRecallRetriever with canned
// hits so the tool-level tests can exercise the rendering and validation
// surfaces without dragging in the tools→memory→session→tools import
// cycle. Production wire-in (see internal/app/runtime.go) wraps
// *memory.Retriever.Tier3AtomicRecall to satisfy the same interface.
type stubMemoryRecallRetriever struct {
	hits      []MemoryRecallHit
	err       error
	calls     int
	lastQuery string
	lastTopK  int
	lastScope MemoryRecallScope
}

func (s *stubMemoryRecallRetriever) Tier3AtomicRecall(_ context.Context, query string, topK int, scope MemoryRecallScope) ([]MemoryRecallHit, error) {
	s.calls++
	s.lastQuery = query
	s.lastTopK = topK
	s.lastScope = scope
	if s.err != nil {
		return nil, s.err
	}
	return append([]MemoryRecallHit(nil), s.hits...), nil
}

// singleRecallHit is the canned hit the happy-path tests assert against.
// Authority / evidence / score mirror the spec §6.1 weighting schema so
// the rendered line exercises every template field.
func singleRecallHit() MemoryRecallHit {
	return MemoryRecallHit{
		ID:            "mem_abc123def4567890",
		Claim:         "项目测试入口是 go test ./...",
		Authority:     "explicit",
		SourceRef:     "session:42:user",
		EvidenceScore: 1.0,
		Score:         2.45,
	}
}

// TestMemoryRecallToolExecutes covers spec §6.2: the Agent can invoke the
// memory_recall tool mid-run to do Tier 3 atomic FTS5 recall on a free-form
// query. The brief pre-builts a PreparedCall (skipping Prepare) so the test
// only exercises the Execute path: under effect=state the tool must not
// require a Capability, and the rendered markdown bullet list must carry
// at least one memory id so the Agent has something to cite.
func TestMemoryRecallToolExecutes(t *testing.T) {
	stub := &stubMemoryRecallRetriever{hits: []MemoryRecallHit{singleRecallHit()}}
	tool := NewMemoryRecallTool(stub)

	guard, err := platform.NewPathGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewPathGuard: %v", err)
	}
	rawArg := []byte(`{"query":"test","topK":3}`)
	result, err := tool.Execute(context.Background(), &PreparedCall{
		ToolName:     "memory_recall",
		ID:           "t-1",
		CanonicalArg: rawArg,
		Effects:      []Effect{EffectState},
		Digest:       ComputeDigest("memory_recall", rawArg),
	}, Capability{}, ExecEnv{Guard: guard})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "mem_") {
		t.Fatalf("expected memory id in output, got %s", result.Output)
	}
	if result.Metadata["query"] != "test" {
		t.Fatalf("metadata.query=%q, want %q", result.Metadata["query"], "test")
	}
	if result.Metadata["topK"] != "3" {
		t.Fatalf("metadata.topK=%q, want %q", result.Metadata["topK"], "3")
	}
	if result.Metadata["count"] != "1" {
		t.Fatalf("metadata.count=%q, want 1", result.Metadata["count"])
	}
	if stub.calls != 1 || stub.lastQuery != "test" || stub.lastTopK != 3 {
		t.Fatalf("retriever call mismatch: calls=%d query=%q topK=%d",
			stub.calls, stub.lastQuery, stub.lastTopK)
	}
	if stub.lastScope.Kind != "user" {
		t.Fatalf("default scope kind=%q, want %q", stub.lastScope.Kind, "user")
	}
}

// TestMemoryRecallToolOmitsCapabilities locks in the spec §6.2 "no approval
// needed" property: a state-effect tool must not call CheckCapability and
// must therefore never surface ErrCapabilityMissing even when the caller
// passes a zero Capability. The test passes through the full Execute
// path; if CheckCapability were ever re-introduced it would fail with
// ErrCapabilityMissing because there is no CapabilityVerifier wired in.
func TestMemoryRecallToolOmitsCapabilities(t *testing.T) {
	stub := &stubMemoryRecallRetriever{}
	tool := NewMemoryRecallTool(stub)

	guard, err := platform.NewPathGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewPathGuard: %v", err)
	}
	rawArg := []byte(`{"query":"test"}`)
	call := &PreparedCall{
		ToolName:     "memory_recall",
		ID:           "t-2",
		CanonicalArg: rawArg,
		Effects:      []Effect{EffectState},
		Digest:       ComputeDigest("memory_recall", rawArg),
	}
	if _, err := tool.Execute(context.Background(), call, Capability{}, ExecEnv{Guard: guard}); err != nil {
		t.Fatalf("Execute() with zero Capability error = %v, want nil (state effect)", err)
	}
}

// TestMemoryRecallToolFormatsMarkdownBullets locks in the spec §6.2 output
// format `- {id} (authority={a}, evidence={e:.2f}, score={s:.2f}) {claim}
// [src: {ref}]` so the Agent can both cite the id and audit the trust
// weighting without a second hop into `mengdie memory show <id>`. The empty
// result must render the literal "(empty)" sentinel so the Agent can
// distinguish "no match" from a failed recall.
func TestMemoryRecallToolFormatsMarkdownBullets(t *testing.T) {
	t.Run("populated", func(t *testing.T) {
		stub := &stubMemoryRecallRetriever{hits: []MemoryRecallHit{singleRecallHit()}}
		tool := NewMemoryRecallTool(stub)
		guard, err := platform.NewPathGuard(t.TempDir())
		if err != nil {
			t.Fatalf("NewPathGuard: %v", err)
		}
		rawArg := []byte(`{"query":"go","topK":5}`)
		call := &PreparedCall{
			ToolName:     "memory_recall",
			ID:           "t-3",
			CanonicalArg: rawArg,
			Effects:      []Effect{EffectState},
			Digest:       ComputeDigest("memory_recall", rawArg),
		}
		result, err := tool.Execute(context.Background(), call, Capability{}, ExecEnv{Guard: guard})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(result.Output, "- ") {
			t.Fatalf("output must lead with markdown bullet, got %q", result.Output)
		}
		for _, want := range []string{
			"mem_abc123def4567890",
			"authority=explicit",
			"evidence=1.00",
			"score=2.45",
			"项目测试入口是 go test ./...",
			"[src: session:42:user]",
		} {
			if !strings.Contains(result.Output, want) {
				t.Fatalf("output missing %q:\n%s", want, result.Output)
			}
		}
	})

	t.Run("empty", func(t *testing.T) {
		stub := &stubMemoryRecallRetriever{hits: nil}
		tool := NewMemoryRecallTool(stub)
		guard, err := platform.NewPathGuard(t.TempDir())
		if err != nil {
			t.Fatalf("NewPathGuard: %v", err)
		}
		rawArg := []byte(`{"query":"nonexistent"}`)
		call := &PreparedCall{
			ToolName:     "memory_recall",
			ID:           "t-4",
			CanonicalArg: rawArg,
			Effects:      []Effect{EffectState},
			Digest:       ComputeDigest("memory_recall", rawArg),
		}
		result, err := tool.Execute(context.Background(), call, Capability{}, ExecEnv{Guard: guard})
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(result.Output) != "(empty)" {
			t.Fatalf("empty result must be %q, got %q", "(empty)", result.Output)
		}
		if result.Metadata["count"] != "0" {
			t.Fatalf("count metadata=%q, want 0", result.Metadata["count"])
		}
	})
}

// TestMemoryRecallToolDefaultsTopKFromSpec covers the spec §6.2 default
// topK=5: a Prepare call omitting topK must canonicalize to the default so
// two calls (one explicit, one implicit) compute the same digest and stay
// idempotent through Policy / Approval replay.
func TestMemoryRecallToolDefaultsTopKFromSpec(t *testing.T) {
	stub := &stubMemoryRecallRetriever{}
	tool := NewMemoryRecallTool(stub)

	guard, err := platform.NewPathGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewPathGuard: %v", err)
	}
	prepared, err := tool.Prepare(context.Background(), []byte(`{"query":"go"}`),
		PrepareEnv{CallID: "call-1", Guard: guard})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !strings.Contains(string(prepared.CanonicalArg), `"topK":5`) &&
		!strings.Contains(string(prepared.CanonicalArg), `"topK": 5`) {
		t.Fatalf("canonical args missing default topK=5: %s", prepared.CanonicalArg)
	}
	if got := prepared.Digest; got != ComputeDigest("memory_recall", prepared.CanonicalArg) {
		t.Fatalf("digest mismatch: %s vs %s", got, ComputeDigest("memory_recall", prepared.CanonicalArg))
	}
}

// TestMemoryRecallToolRejectsInvalidArgs locks the input validation rules
// from the input schema: query is required, topK must be in [1, 50], and
// unknown fields are rejected. Each branch uses Prepare so the model-side
// validation path is exercised independently from Execute.
//
// topK=0 / missing topK is intentionally NOT in this list — the spec §6.2
// default of 5 is applied silently in that case to keep the canonical
// arg stable across "topK omitted" and "topK explicitly 5" callers.
func TestMemoryRecallToolRejectsInvalidArgs(t *testing.T) {
	stub := &stubMemoryRecallRetriever{}
	tool := NewMemoryRecallTool(stub)

	guard, err := platform.NewPathGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewPathGuard: %v", err)
	}
	env := PrepareEnv{CallID: "call-1", Guard: guard}

	cases := map[string]string{
		"missing-query":  `{"topK":3}`,
		"blank-query":    `{"query":"   ","topK":3}`,
		"topK-too-large": `{"query":"go","topK":51}`,
		"topK-negative":  `{"query":"go","topK":-1}`,
		"unknown-field":  `{"query":"go","flag":true}`,
		"bad-json":       `{"query":`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := tool.Prepare(context.Background(), []byte(raw), env); err == nil {
				t.Fatalf("Prepare(%s) succeeded", raw)
			}
		})
	}
}

// TestMemoryRecallToolScopesByProjectIdentity locks in the spec §6.2 scope
// rule: when the caller supplies a non-empty ProjectIdentity the tool must
// forward scope={project, id} to the retriever; empty ProjectIdentity
// keeps the default user scope so unit tests / safe-by-default call paths
// do not silently over-narrow the recall.
func TestMemoryRecallToolScopesByProjectIdentity(t *testing.T) {
	stub := &stubMemoryRecallRetriever{}
	tool := NewMemoryRecallTool(stub, WithProjectIdentity("mengdie"))

	guard, err := platform.NewPathGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewPathGuard: %v", err)
	}
	rawArg := []byte(`{"query":"go","topK":5}`)
	if _, err := tool.Execute(context.Background(), &PreparedCall{
		ToolName:     "memory_recall",
		ID:           "t-scope",
		CanonicalArg: rawArg,
		Effects:      []Effect{EffectState},
		Digest:       ComputeDigest("memory_recall", rawArg),
	}, Capability{}, ExecEnv{Guard: guard}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stub.lastScope.Kind != "project" || stub.lastScope.Value != "mengdie" {
		t.Fatalf("scope = {%q, %q}, want {project, mengdie}", stub.lastScope.Kind, stub.lastScope.Value)
	}
}

// TestMemoryRecallToolSurfacesRetrieverErrors confirms the tool propagates
// errors from the retriever unchanged so Agent-level handling can branch
// on errors.Is/As without first unwrapping a tool-local wrapper.
func TestMemoryRecallToolSurfacesRetrieverErrors(t *testing.T) {
	want := errors.New("fts5 backend down")
	stub := &stubMemoryRecallRetriever{err: want}
	tool := NewMemoryRecallTool(stub)

	guard, err := platform.NewPathGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewPathGuard: %v", err)
	}
	rawArg := []byte(`{"query":"go","topK":5}`)
	got, err := tool.Execute(context.Background(), &PreparedCall{
		ToolName:     "memory_recall",
		ID:           "t-err",
		CanonicalArg: rawArg,
		Effects:      []Effect{EffectState},
		Digest:       ComputeDigest("memory_recall", rawArg),
	}, Capability{}, ExecEnv{Guard: guard})
	if err == nil {
		t.Fatalf("Execute() result=%+v, want error", got)
	}
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want chain containing %v", err, want)
	}
}
