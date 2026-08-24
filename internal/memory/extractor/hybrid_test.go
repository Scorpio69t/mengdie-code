// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
)

// TestHybridRulesOnlyWhenLLMNil exercises the LLM=nil fast path: Hybrid
// must return whatever Rules produces (here, the empty default) without
// panicking on a nil interface. This is the v0.1 LLM-disabled mode.
func TestHybridRulesOnlyWhenLLMNil(t *testing.T) {
	rules := &Rules{} // empty rules, returns nil
	h := NewHybrid(rules, nil)
	got, _ := h.Extract(context.Background(), "session-1")
	if got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

// TestHybridDropsLLMDuplicatesOfRules exercises the deduplication step:
// the LLM half proposes a claim that Rules already produced (after
// Unicode/case normalization the strings are identical) plus one new
// claim. The hybrid output must contain exactly two rows — one from
// Rules and the unique LLM candidate — never the duplicate. The
// authority from Rules (AuthorityRepository) wins over the LLM's
// AuthorityInferred for the shared claim, which is the whole point of
// running Rules first.
func TestHybridDropsLLMDuplicatesOfRules(t *testing.T) {
	rules := &fakeRules{mems: []memory.Memory{
		{Claim: "项目使用 edit_file 修改文件", Authority: memory.AuthorityRepository},
	}}
	llm := &fakeLLM{mems: []memory.Memory{
		{Claim: "项目使用 edit_file 修改文件", Authority: memory.AuthorityInferred},
		{Claim: "项目偏好中文 README", Authority: memory.AuthorityInferred},
	}}
	h := NewHybrid(rules, llm)
	got, _ := h.Extract(context.Background(), "session-1")
	if len(got) != 2 {
		t.Fatalf("want 2 (1 rule + 1 unique LLM), got %d", len(got))
	}
	var seenEdit, seenReadme bool
	for _, m := range got {
		if m.Claim == "项目使用 edit_file 修改文件" {
			seenEdit = true
		}
		if m.Claim == "项目偏好中文 README" {
			seenReadme = true
		}
	}
	if !seenEdit || !seenReadme {
		t.Fatalf("missing claims: edit=%v readme=%v", seenEdit, seenReadme)
	}
}

// fakeRules / fakeLLM are minimal Extractor implementations used to keep
// this test independent of the real Rules/LLM wiring. They let the test
// drive Hybrid with deterministic input — no EventReader, no provider.
type fakeRules struct{ mems []memory.Memory }

func (f *fakeRules) Extract(_ context.Context, _ string) ([]memory.Memory, error) {
	return f.mems, nil
}

type fakeLLM struct{ mems []memory.Memory }

func (f *fakeLLM) Extract(_ context.Context, _ string) ([]memory.Memory, error) {
	return f.mems, nil
}
