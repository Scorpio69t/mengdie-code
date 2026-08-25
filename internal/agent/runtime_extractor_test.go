// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

// stubStore is the test double for the agent.memoryWriter surface used
// by applyMemoryExtraction. It records every ProposeMemory and Approve
// call so the two-phase extraction contract can be asserted without
// spinning up a SQLite session: the production wiring still uses the
// real *memory.Store (see newTestAgentWithExtractor), but tests for
// the call-order and per-row auto-approval logic substitute this stub
// to keep the assertions focused on the hook's branch behaviour rather
// than on SQLite row state.
//
// ProposeMemory stamps each returned memory with a deterministic
// synthetic id so Approve's id-only signature can be matched back to
// the proposed row in the test assertions.
type stubStore struct {
	proposeCount int
	approveCount int
	proposed     []memory.Memory
	approvedIDs  []string
	proposeErrAt int // 1-based index; 0 means never error
	approveErrAt int // 1-based index; 0 means never error
	// conflictFn lets tests inject a custom IsCrossAuthorityConflict
	// response. When nil the stub reports no conflict (false, nil) so
	// existing tests continue to see the slice-03 auto-Approve path.
	conflictFn func(context.Context, memory.Memory) (bool, error)
}

func (s *stubStore) ProposeMemory(_ context.Context, m memory.Memory) (memory.Memory, error) {
	s.proposeCount++
	if s.proposeErrAt > 0 && s.proposeCount == s.proposeErrAt {
		return memory.Memory{}, errors.New("stub propose failure")
	}
	if strings.TrimSpace(m.ID) == "" {
		m.ID = fmt.Sprintf("stub-mem-%d", s.proposeCount)
	}
	s.proposed = append(s.proposed, m)
	return m, nil
}

func (s *stubStore) Approve(_ context.Context, id string) error {
	s.approveCount++
	if s.approveErrAt > 0 && s.approveCount == s.approveErrAt {
		return errors.New("stub approve failure")
	}
	s.approvedIDs = append(s.approvedIDs, id)
	return nil
}

func (s *stubStore) IsCrossAuthorityConflict(ctx context.Context, m memory.Memory) (bool, error) {
	if s.conflictFn != nil {
		return s.conflictFn(ctx, m)
	}
	return false, nil
}

// newTestAgentWithExtractorAndStore mirrors newTestAgentWithExtractor
// but lets the test substitute the memoryWriter surface. The session
// SQLite store is still opened (and torn down via t.Cleanup) so the
// Provider / Registry / Policy plumbing stays identical to the real
// path; only the writer is stubbed.
func newTestAgentWithExtractorAndStore(t *testing.T, ext MemoryExtractor, store memoryWriter) *Agent {
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
	// Touch the real *memory.Store so its package stays linked and
	// future regressions in the wiring surface immediately. The stub
	// is what applyMemoryExtraction actually calls.
	_ = memory.OpenMemory(sessionStore)

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
		MemoryStore:      store,
		MemoryExtractor:  ext,
	})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

// TestRunAppliesExtractionTwoPhaseWithAutoApprove pins the M3 Slice 03
// two-phase contract for the post-Run extraction hook:
//
//  1. Every candidate goes through ProposeMemory (both proposed rows
//     must show up in the stub store).
//  2. Only candidates whose Claim matches a fingerprint pattern (here
//     "edit_file") are auto-promoted via Approve; the other candidate
//     stays at the ProposeMemory step.
//  3. RunResult.AutoApprovedCount mirrors the number of Approve calls.
//
// The fingerprint claim matches extractor.isProjectUsesEditFile; the
// non-fingerprint claim is generic enough to clear none of the
// whitelist patterns. The stub's count fields let us assert the call
// totals, and the proposed / approvedIDs slices let us assert that the
// right row was promoted by id-matching Approve back to the stored
// Memory.
func TestRunAppliesExtractionTwoPhaseWithAutoApprove(t *testing.T) {
	const fingerprintClaim = "项目使用 edit_file 修改文件"
	const manualClaim = "用户偏好每次都跑 npm test"
	stub := &stubExtractor{mems: []memory.Memory{
		{Claim: fingerprintClaim, Authority: memory.AuthorityInferred},
		{Claim: manualClaim, Authority: memory.AuthorityInferred},
	}}
	store := &stubStore{}
	a := newTestAgentWithExtractorAndStore(t, stub, store)

	result, err := a.Run(context.Background(), newTestRequest(), newTestEmitter(t))
	if err != nil {
		t.Fatalf("Agent.Run returned error: %v", err)
	}

	if stub.callCount != 1 {
		t.Fatalf("Extract called %d times, want 1", stub.callCount)
	}
	if store.proposeCount != 2 {
		t.Fatalf("ProposeMemory called %d times, want 2 (both candidates)", store.proposeCount)
	}
	if store.approveCount != 1 {
		t.Fatalf("Approve called %d times, want 1 (only fingerprint candidate)", store.approveCount)
	}
	if len(store.proposed) != 2 {
		t.Fatalf("len(proposed) = %d, want 2", len(store.proposed))
	}
	if len(store.approvedIDs) != 1 {
		t.Fatalf("len(approvedIDs) = %d, want 1", len(store.approvedIDs))
	}

	// Match the approved id back to one of the proposed rows; the
	// row whose Claim is the fingerprint claim must be the one that
	// was promoted.
	var promotedClaim string
	for _, mem := range store.proposed {
		if mem.ID == store.approvedIDs[0] {
			promotedClaim = mem.Claim
			break
		}
	}
	if promotedClaim != fingerprintClaim {
		t.Fatalf("promoted claim = %q, want %q", promotedClaim, fingerprintClaim)
	}

	// Manual-review row must still be in the proposed slice (the
	// hook must NOT touch it beyond ProposeMemory).
	var manualProposed bool
	for _, mem := range store.proposed {
		if mem.Claim == manualClaim {
			manualProposed = true
			break
		}
	}
	if !manualProposed {
		t.Fatalf("manual candidate %q missing from proposed slice", manualClaim)
	}

	if result.AutoApprovedCount != 1 {
		t.Fatalf("RunResult.AutoApprovedCount = %d, want 1", result.AutoApprovedCount)
	}
}

// TestRunAppliesExtractionSkipsAutoApproveOnCrossAuthorityConflict pins
// the M3 Slice 04 safety gate: a fingerprint-matching candidate that has
// a cross-authority dispute (per memory.Store.IsCrossAuthorityConflict,
// spec §4.2 row 3) MUST NOT be auto-promoted from StatusProposed to
// StatusActive. It stays StatusProposed for human review. The hook
// must:
//
//  1. ProposeMemory both candidates (the dispute does not block the
//     propose-time path).
//  2. Skip the Approve call when the conflict check returns true.
//  3. Leave result.AutoApprovedCount at 0.
//
// conflictFn discriminates by claim so the fingerprint candidate is
// the one blocked while the manual-review candidate is allowed to
// fall through the non-fingerprint branch (it would never auto-Approve
// anyway, but the assertion is still that Approve is not called for
// either row). This pins the "fingerprint auto-Approve must NOT bypass
// higher-authority active memories" invariant from the Slice 04 brief.
func TestRunAppliesExtractionSkipsAutoApproveOnCrossAuthorityConflict(t *testing.T) {
	const fingerprintClaim = "项目使用 edit_file 修改文件"
	const manualClaim = "用户偏好每次都跑 npm test"
	stub := &stubExtractor{mems: []memory.Memory{
		{Claim: fingerprintClaim, Authority: memory.AuthorityInferred},
		{Claim: manualClaim, Authority: memory.AuthorityInferred},
	}}
	store := &stubStore{
		conflictFn: func(_ context.Context, m memory.Memory) (bool, error) {
			return m.Claim == fingerprintClaim, nil
		},
	}
	a := newTestAgentWithExtractorAndStore(t, stub, store)

	result, err := a.Run(context.Background(), newTestRequest(), newTestEmitter(t))
	if err != nil {
		t.Fatalf("Agent.Run returned error: %v", err)
	}

	if stub.callCount != 1 {
		t.Fatalf("Extract called %d times, want 1", stub.callCount)
	}
	if store.proposeCount != 2 {
		t.Fatalf("ProposeMemory called %d times, want 2 (both candidates)", store.proposeCount)
	}
	if store.approveCount != 0 {
		t.Fatalf("Approve called %d times, want 0 (fingerprint candidate blocked by conflict)", store.approveCount)
	}
	if len(store.proposed) != 2 {
		t.Fatalf("len(proposed) = %d, want 2", len(store.proposed))
	}
	if len(store.approvedIDs) != 0 {
		t.Fatalf("len(approvedIDs) = %d, want 0", len(store.approvedIDs))
	}

	// The fingerprint candidate must remain in the proposed slice as
	// StatusProposed; the hook must not have promoted it.
	var fingerprintProposed bool
	for _, mem := range store.proposed {
		if mem.Claim == fingerprintClaim {
			fingerprintProposed = true
			break
		}
	}
	if !fingerprintProposed {
		t.Fatalf("fingerprint candidate %q missing from proposed slice", fingerprintClaim)
	}

	// Manual-review candidate must also still be in the proposed slice
	// (it would never auto-Approve, but the assertion pins that the
	// hook still ran ProposeMemory for it).
	var manualProposed bool
	for _, mem := range store.proposed {
		if mem.Claim == manualClaim {
			manualProposed = true
			break
		}
	}
	if !manualProposed {
		t.Fatalf("manual candidate %q missing from proposed slice", manualClaim)
	}

	if result.AutoApprovedCount != 0 {
		t.Fatalf("RunResult.AutoApprovedCount = %d, want 0 (conflict blocked the only fingerprint candidate)", result.AutoApprovedCount)
	}
}
