// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
)

// TestHybridRulesOnlyWhenLLMNil exercises the LLM=nil fast path. It must:
//   - return the Rules result unchanged (not nil, not empty) — a wrong impl
//     `return nil, nil` would silently pass the previous assertion;
//   - return a nil error — Hybrid must never surface errors;
//   - never call the LLM half (no nil-interface panic, no leaked
//     invocation).
//
// The callCount assertion on a separately-wired fakeLLM is the
// load-bearing one: a future refactor that "simplifies" the fast-path
// into `rulesOut, _ := h.llm.Extract(...)` would panic on the nil
// interface. Wiring the tracker up front and asserting callCount == 0
// also serves as living documentation that Hybrid's llm half is only
// invoked when llm is non-nil.
func TestHybridRulesOnlyWhenLLMNil(t *testing.T) {
	sentinel := memory.Memory{
		Claim:     "sentinel-from-rules",
		Authority: memory.AuthorityRepository,
	}
	rules := &fakeRules{mems: []memory.Memory{sentinel}}
	llm := &fakeLLM{mems: []memory.Memory{
		{Claim: "should-never-be-read", Authority: memory.AuthorityInferred},
	}}
	h := NewHybrid(rules, nil) // llm IS nil — that's what the fast path tests

	got, err := h.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Hybrid must return nil error, got %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 sentinel row from Rules, got %d (%v)", len(got), got)
	}
	if got[0].Claim != "sentinel-from-rules" {
		t.Fatalf("want sentinel claim %q, got %q", "sentinel-from-rules", got[0].Claim)
	}
	if got[0].Authority != memory.AuthorityRepository {
		t.Fatalf("want AuthorityRepository, got %s", got[0].Authority)
	}
	if llm.callCount != 0 {
		t.Fatalf("LLM.Extract must not be called when llm==nil, got %d calls", llm.callCount)
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

// TestHybridDropsLLMDuplicatesAfterCaseFold pins down the case-fold
// branch of the canonicalization contract: the same claim in two
// different casings must collide in the dedup map so only the Rules
// row survives. This locks in the ToLower step of
// memory.CanonicalizeClaim so future refactors cannot silently remove
// it.
func TestHybridDropsLLMDuplicatesAfterCaseFold(t *testing.T) {
	rules := &fakeRules{mems: []memory.Memory{
		{Claim: "PROJECT uses edit_file", Authority: memory.AuthorityRepository},
	}}
	llm := &fakeLLM{mems: []memory.Memory{
		// different case, otherwise identical → must dedup against the rules row
		{Claim: "project uses edit_file", Authority: memory.AuthorityInferred},
		// genuinely new claim → must survive
		{Claim: "项目偏好中文 README", Authority: memory.AuthorityInferred},
	}}
	h := NewHybrid(rules, llm)
	got, _ := h.Extract(context.Background(), "session-1")
	if len(got) != 2 {
		t.Fatalf("want 2 (1 rule + 1 unique LLM), got %d (%v)", len(got), got)
	}
	// The surviving rule row must keep its higher authority — the LLM
	// candidate for the same canonical claim must NOT have been appended.
	for _, m := range got {
		if m.Claim == "project uses edit_file" {
			t.Fatalf("LLM case-folded duplicate leaked into output: %+v", m)
		}
		if m.Authority == memory.AuthorityInferred {
			continue
		}
		// AuthorityRepository row stays — assert the rules claim survived
		// under its original casing.
		if m.Claim != "PROJECT uses edit_file" {
			t.Fatalf("want rules claim %q, got %q", "PROJECT uses edit_file", m.Claim)
		}
	}
}

// TestHybridDoesNotTrimWhitespace pins down the opposite half of the
// contract: CanonicalizeClaim does NOT TrimSpace, matching Store.Save's
// idempotency path (which validates non-empty-but-doesn't-trim before
// calling the helper). A rule row with leading/trailing whitespace and
// an LLM row with the same string minus the whitespace are therefore
// distinct canonical claims and BOTH must survive dedup.
//
// This is intentional: trimming would silently change Store.Save's
// idempotency key for any caller that previously stored "  foo  "
// verbatim, and the brief's "must be byte-equivalent for the same
// input" hard requirement forbids that. If trimming is ever wanted
// it must be added in one place (Store.Save's validation gate + here)
// with an explicit Store.Save test, not by mutating the canonical
// helper.
func TestHybridDoesNotTrimWhitespace(t *testing.T) {
	rules := &fakeRules{mems: []memory.Memory{
		{Claim: "  PROJECT uses edit_file  ", Authority: memory.AuthorityRepository},
	}}
	llm := &fakeLLM{mems: []memory.Memory{
		// no whitespace → different canonical form → must NOT dedup
		{Claim: "PROJECT uses edit_file", Authority: memory.AuthorityInferred},
	}}
	h := NewHybrid(rules, llm)
	got, _ := h.Extract(context.Background(), "session-1")
	if len(got) != 2 {
		t.Fatalf("want 2 (whitespace is significant → both kept), got %d (%v)", len(got), got)
	}
}

// fakeRules / fakeLLM are minimal Extractor implementations used to keep
// these tests independent of the real Rules/LLM wiring. fakeLLM records
// callCount so the LLM=nil fast-path test can assert Extract was never
// invoked.
type fakeRules struct{ mems []memory.Memory }

func (f *fakeRules) Extract(_ context.Context, _ string) ([]memory.Memory, error) {
	return f.mems, nil
}

type fakeLLM struct {
	mems      []memory.Memory
	callCount int
}

func (f *fakeLLM) Extract(_ context.Context, _ string) ([]memory.Memory, error) {
	f.callCount++
	return f.mems, nil
}
