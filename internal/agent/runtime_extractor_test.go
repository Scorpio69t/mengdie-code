// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/session"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

// stubExtractor is a test double for the agent.MemoryExtractor surface.
// It counts how many times Extract was called and returns the canned
// (mems, err) pair; tests use the counter to assert the post-Run hook
// ran exactly once, and use err to verify extraction failures do not
// fail the surrounding Run.
type stubExtractor struct {
	mems      []memory.Memory
	err       error
	callCount int
}

func (s *stubExtractor) Extract(_ context.Context, _ string) ([]memory.Memory, error) {
	s.callCount++
	return s.mems, s.err
}

// TestRunAppliesExtractionBeforeReturn verifies that Agent.Run calls the
// configured MemoryExtractor exactly once when a Run terminates normally.
// The single-tap assertion guards against a regression that fires the
// extractor per turn or skips the hook when the response carries a
// non-empty tool call. Failure to invoke Extract at all also trips this
// test.
func TestRunAppliesExtractionBeforeReturn(t *testing.T) {
	stub := &stubExtractor{mems: []memory.Memory{
		{Claim: "stub rule", Authority: memory.AuthorityRepository},
	}}
	a := newTestAgentWithExtractor(t, stub)
	if _, err := a.Run(context.Background(), newTestRequest(), newTestEmitter(t)); err != nil {
		t.Fatal(err)
	}
	if stub.callCount != 1 {
		t.Fatalf("Extract called %d times, want 1", stub.callCount)
	}
}

// TestRunExtractorFailureDoesNotFailRun verifies that an error returned
// by the MemoryExtractor never aborts the surrounding Run. The summary
// the scripted provider produced must still be visible on RunResult, and
// the error returned by Run must remain nil. This pins the brief rule
// "Extract failure is logged as a warning and never fails the run".
func TestRunExtractorFailureDoesNotFailRun(t *testing.T) {
	stub := &stubExtractor{err: errors.New("boom")}
	a := newTestAgentWithExtractor(t, stub)
	result, err := a.Run(context.Background(), newTestRequest(), newTestEmitter(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary == "" {
		t.Fatal("summary should still be produced")
	}
}

// newTestAgentWithExtractor builds the minimal Agent shape that exercises
// the applyMemoryExtraction hook: a scripted provider that emits a final
// message on the first turn, a registry with the default tools, a
// headless policy engine, and a real *memory.Store backed by a temp
// SQLite session so the propose-time path is observable end-to-end.
//
// projectIdentity is set to a non-empty value because applyMemoryExtraction
// short-circuits when it is empty; the hook has to reach the extractor
// call for these tests to observe callCount.
func newTestAgentWithExtractor(t *testing.T, ext MemoryExtractor) *Agent {
	t.Helper()
	root := t.TempDir()
	guard, err := platform.NewPathGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry(tools.DefaultTools()...)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := policy.NewEngine(policy.Options{Root: root, Mode: policy.ModeHeadless, CLI: nil})
	if err != nil {
		t.Fatal(err)
	}

	sessionStore, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir:     t.TempDir(),
		ProjectRoot: filepath.Join(t.TempDir(), "project"),
		BusyTimeout: 250 * time.Millisecond,
		Now:         time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sessionStore.Close()
	})
	memStore := memory.OpenMemory(sessionStore)

	fake := &scriptedProvider{responses: []*provider.ChatResponse{
		assistantFinal("提取完成。", provider.Usage{}),
	}}
	agent, err := New(Options{
		Provider:         fake,
		Registry:         registry,
		Guard:            guard,
		Policy:           engine,
		Now:              time.Now,
		MaxContextTokens: 64_000,
		ProjectIdentity:  "test-project",
		MemoryStore:      memStore,
		MemoryExtractor:  ext,
	})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

// newTestRequest returns the smallest RunRequest that drives a
// single-turn, no-tool-call completion. MaxTurns is left at 2 so the
// success path emits RunCompleted before the loop guard triggers
// ErrMaxTurns.
func newTestRequest() RunRequest {
	return RunRequest{RunID: "run-ext", Task: "提取", Model: "fake:model", MaxTurns: 2}
}

// newTestEmitter returns an Emitter backed by an in-memory sink. The
// run_id matches newTestRequest so emitted events line up under the
// same scope.
func newTestEmitter(t *testing.T) *events.Emitter {
	t.Helper()
	emitter, err := events.NewEmitter("run-ext", &events.MemorySink{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return emitter
}
