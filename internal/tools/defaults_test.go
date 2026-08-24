// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"
)

// TestDefaultToolsNoOptionsReturnsBaseTools guards the variadic-options
// refactor: DefaultTools() with zero args must stay byte-compatible with
// the pre-refactor implementation and never append memory_recall.
func TestDefaultToolsNoOptionsReturnsBaseTools(t *testing.T) {
	got := DefaultTools()
	for _, tool := range got {
		if tool.Spec().Name == MemoryRecallToolName {
			t.Fatalf("memory_recall should NOT be in default tools without WithMemoryRetriever")
		}
	}
}

// TestDefaultToolsWithMemoryRetrieverAddsMemoryRecall verifies the opt-in
// path: passing WithMemoryRetriever appends memory_recall to the base
// set. stubRetriever is a no-op implementation that satisfies
// MemoryRecallRetriever so the option's registration path runs end-to-end
// without depending on real memory infrastructure.
func TestDefaultToolsWithMemoryRetrieverAddsMemoryRecall(t *testing.T) {
	stub := &stubRetriever{}
	got := DefaultTools(WithMemoryRetriever(stub))
	found := false
	for _, tool := range got {
		if tool.Spec().Name == MemoryRecallToolName {
			found = true
		}
	}
	if !found {
		t.Fatal("memory_recall should be in default tools when WithMemoryRetriever is set")
	}
}

// stubRetriever satisfies MemoryRecallRetriever with no behavior. Tests
// that route it through WithMemoryRetriever only assert registration,
// not retrieval semantics; memory_recall's Execute path is covered by
// memory_recall_test.go.
type stubRetriever struct{}

func (stubRetriever) Tier3AtomicRecall(ctx context.Context, query string, topK int, scope MemoryRecallScope) ([]MemoryRecallHit, error) {
	return nil, nil
}
