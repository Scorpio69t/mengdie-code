// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package chaos

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/session"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func TestNewRejectsUnknownHook(t *testing.T) {
	_, err := New(Schedule{Fires: []PlannedFire{{Hook: Hook("nope"), FireKind: FireAbort}}})
	if err == nil {
		t.Fatal("expected error for unknown hook")
	}
}

func TestNewRejectsUnknownFireKind(t *testing.T) {
	_, err := New(Schedule{Fires: []PlannedFire{{Hook: HookEventStoreCommit, FireKind: FireKind("nope"), AfterSeq: 1}}})
	if err == nil {
		t.Fatal("expected error for unknown fire kind")
	}
}

func TestControllerFireOnceOnFirstOccurrence(t *testing.T) {
	ctrl, err := New(Schedule{Fires: []PlannedFire{{Hook: HookContextSummary, FireKind: FireAbort}}})
	if err != nil {
		t.Fatal(err)
	}
	decision := ctrl.MaybeFire(HookContextSummary, 0, "summary", false)
	if !decision.Observed || decision.Fire != FireAbort {
		t.Fatalf("first call should fire abort, got %+v", decision)
	}
	second := ctrl.MaybeFire(HookContextSummary, 0, "summary", false)
	if second.Observed {
		t.Fatalf("second call should not fire, got %+v", second)
	}
	if len(ctrl.Observations()) != 1 {
		t.Fatalf("expected one observation, got %d", len(ctrl.Observations()))
	}
}

func TestControllerEventStoreCommitMatchesAfterSeq(t *testing.T) {
	ctrl, err := New(Schedule{Fires: []PlannedFire{{Hook: HookEventStoreCommit, FireKind: FireAbort, AfterSeq: 5}}})
	if err != nil {
		t.Fatal(err)
	}
	miss := ctrl.MaybeFire(HookEventStoreCommit, 4, "tool.completed", false)
	if miss.Observed {
		t.Fatal("seq 4 should not match AfterSeq=5")
	}
	hit := ctrl.MaybeFire(HookEventStoreCommit, 5, "tool.completed", false)
	if !hit.Observed || hit.Fire != FireAbort {
		t.Fatalf("seq 5 should match, got %+v", hit)
	}
}

func TestControllerArmOverridesSchedule(t *testing.T) {
	ctrl, err := New(Schedule{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Arm(HookPendingApproval, FireContext); err != nil {
		t.Fatal(err)
	}
	decision := ctrl.MaybeFire(HookPendingApproval, 0, "edit_file", false)
	if !decision.Observed || !errors.Is(decision.Err, ErrChaosContextCanceled) {
		t.Fatalf("expected context fire, got %+v", decision)
	}
	second := ctrl.MaybeFire(HookPendingApproval, 0, "edit_file", false)
	if second.Observed {
		t.Fatal("armed fire should be single-shot")
	}
}

func TestSinkForwardsByDefault(t *testing.T) {
	mem := &events.MemorySink{}
	ctrl, _ := New(Schedule{})
	sink := NewSink(mem, ctrl)
	event := mustEvent(t)
	if err := sink.Emit(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if got := mem.Events(); len(got) != 1 || got[0].Seq != event.Seq {
		t.Fatalf("memory sink expected one event, got %+v", got)
	}
}

func TestSinkAbortBlocksInnerCommit(t *testing.T) {
	mem := &events.MemorySink{}
	ctrl, _ := New(Schedule{Fires: []PlannedFire{{Hook: HookEventStoreCommit, FireKind: FireAbort, AfterSeq: 1}}})
	sink := NewSink(mem, ctrl)
	if err := sink.Emit(context.Background(), mustEvent(t)); err == nil {
		t.Fatal("expected abort error")
	}
	if len(mem.Events()) != 0 {
		t.Fatalf("inner sink should not receive aborted event, got %d", len(mem.Events()))
	}
	if !ctrl.HasFired() {
		t.Fatal("expected at least one fired observation")
	}
}

func TestSinkContextReturnsSentinel(t *testing.T) {
	mem := &events.MemorySink{}
	ctrl, _ := New(Schedule{Fires: []PlannedFire{{Hook: HookEventStoreCommit, FireKind: FireContext, AfterSeq: 1}}})
	sink := NewSink(mem, ctrl)
	err := sink.Emit(context.Background(), mustEvent(t))
	if err == nil || !strings.Contains(err.Error(), "chaos: context canceled") {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestProviderAbortSkipsInnerStream(t *testing.T) {
	ctrl, _ := New(Schedule{Fires: []PlannedFire{{Hook: HookContextSummary, FireKind: FireAbort}}})
	stub := &stubProvider{}
	p := NewProvider(stub, ctrl)
	if _, err := p.StreamSummary(context.Background(), provider.ChatRequest{Model: "stub"}, nil); err == nil {
		t.Fatal("expected abort error")
	}
	if stub.called {
		t.Fatal("stub provider should not be called on abort")
	}
}

func TestProviderIsSummaryByDefault(t *testing.T) {
	if !defaultIsSummary(provider.ChatRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "summary protocol"},
			{Role: provider.RoleUser, Content: "summarize this"},
		},
	}) {
		t.Fatal("expected system+user request to be treated as summary")
	}
	if defaultIsSummary(provider.ChatRequest{Messages: []provider.Message{{Role: provider.RoleUser, Content: "summarize"}}}) {
		t.Fatal("expected single-message request to not be summary (must include system prompt)")
	}
	if defaultIsSummary(provider.ChatRequest{Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "you are helpful"},
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant, Content: "hello"},
		{Role: provider.RoleUser, Content: "summarize"},
	}}) {
		t.Fatal("expected multi-turn request to not be summary")
	}
	if defaultIsSummary(provider.ChatRequest{
		Tools:    []provider.Tool{{Function: provider.FunctionDefinition{Name: "shell"}}},
		Messages: []provider.Message{{Role: provider.RoleSystem}, {Role: provider.RoleUser}},
	}) {
		t.Fatal("expected request with tools to not be summary")
	}
}

func TestBrokerAbortPreventsDecision(t *testing.T) {
	ctrl, _ := New(Schedule{Fires: []PlannedFire{{Hook: HookPendingApproval, FireKind: FireAbort}}})
	stub := &stubBroker{}
	b := NewBroker(stub, ctrl)
	_, err := b.Decide(context.Background(), policy.ApprovalRequest{Tool: "edit_file"})
	if err == nil {
		t.Fatal("expected abort error")
	}
	if stub.called {
		t.Fatal("stub broker should not be called on abort")
	}
}

func TestJournalPrepareAbort(t *testing.T) {
	ctrl, _ := New(Schedule{Fires: []PlannedFire{{Hook: HookPatchJournalPre, FireKind: FireAbort}}})
	stub := &stubJournal{}
	j := NewJournal(stub, ctrl)
	if _, err := j.Prepare(context.Background(), tools.MutationIntent{ToolName: "edit_file"}); err == nil {
		t.Fatal("expected abort error")
	}
	if stub.prepared {
		t.Fatal("stub journal should not be called on abort")
	}
}

func TestJournalVerifyPostConflictFiresHook(t *testing.T) {
	ctrl, _ := New(Schedule{Fires: []PlannedFire{{Hook: HookPatchJournalConflict, FireKind: FireAbort}}})
	stub := &stubJournal{conflictOnVerify: true}
	j := NewJournal(stub, ctrl)
	err := j.VerifyPost(context.Background(), tools.MutationReceipt{JournalID: "jnl_1"})
	if err == nil {
		t.Fatal("expected conflict error to surface")
	}
	if !ctrl.HasFired() {
		t.Fatal("expected conflict hook to fire")
	}
}

func TestRegistryWrapsEachTool(t *testing.T) {
	root := t.TempDir()
	guard, err := platform.NewPathGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := tools.NewRegistry(stubReadTool())
	if err != nil {
		t.Fatal(err)
	}
	ctrl, _ := New(Schedule{Fires: []PlannedFire{{Hook: HookReadToolPre, FireKind: FireAbort}}})
	reg, err := WrapRegistry(inner, ctrl)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Lookup("read_file")
	if !ok {
		t.Fatal("Lookup missing read_file")
	}
	if _, err := tool.Execute(context.Background(), &tools.PreparedCall{}, tools.Capability{}, tools.ExecEnv{Guard: guard}); err == nil {
		t.Fatal("expected abort error")
	}
	if !ctrl.HasFired() {
		t.Fatal("expected hook to fire")
	}
}

func TestSummaryStable(t *testing.T) {
	ctrl, _ := New(Schedule{Seed: 42, Fires: []PlannedFire{{Hook: HookContextSummary, FireKind: FireAbort}}})
	ctrl.MaybeFire(HookContextSummary, 0, "summary", false)
	snap := ctrl.Snapshot()
	if snap.Seed != 42 {
		t.Fatalf("seed lost in snapshot: %d", snap.Seed)
	}
	if len(snap.Fired) != 1 || snap.Fired[0].Fire != FireAbort {
		t.Fatalf("fired missing: %+v", snap.Fired)
	}
	if len(snap.Remaining) != 0 {
		t.Fatalf("expected no remaining fires, got %+v", snap.Remaining)
	}
}

func TestFactBusRecordsGap(t *testing.T) {
	ctrl, _ := New(Schedule{Fires: []PlannedFire{{Hook: HookTUIFactGap, FireKind: FireAbort, AfterSeq: 7}}})
	bus := NewFactBus(session.NewPublicFactBus(4), ctrl)
	bus.OnGap(7)
	if !ctrl.HasFired() {
		t.Fatal("expected gap fire")
	}
}

func mustEvent(t *testing.T) events.Event {
	t.Helper()
	event, err := events.New("run-1", 1, time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC), events.KindRunStarted, events.RunStarted{Model: "stub"})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

type stubProvider struct {
	called bool
}

func (p *stubProvider) ID() string { return "stub" }
func (p *stubProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{}, nil
}
func (p *stubProvider) Stream(_ context.Context, _ provider.ChatRequest, _ provider.StreamSink) (*provider.ChatResponse, error) {
	p.called = true
	return &provider.ChatResponse{}, nil
}

type stubBroker struct {
	called bool
}

func (b *stubBroker) Decide(context.Context, policy.ApprovalRequest) (policy.ApprovalResponse, error) {
	b.called = true
	return policy.ApprovalResponse{Choice: policy.ApprovalApprove}, nil
}

type stubJournal struct {
	prepared         bool
	conflictOnVerify bool
	mu               sync.Mutex
}

func (s *stubJournal) Prepare(context.Context, tools.MutationIntent) (tools.MutationReceipt, error) {
	s.mu.Lock()
	s.prepared = true
	s.mu.Unlock()
	return tools.MutationReceipt{JournalID: "jnl_stub"}, nil
}
func (s *stubJournal) MarkApplied(context.Context, tools.MutationReceipt) error { return nil }
func (s *stubJournal) VerifyPost(context.Context, tools.MutationReceipt) error {
	if s.conflictOnVerify {
		return tools.ErrMutationConflict
	}
	return nil
}

func stubReadTool() tools.Tool {
	spec := tools.ToolSpec{Name: "read_file", Effects: []tools.Effect{tools.EffectRead}, InputSchema: json.RawMessage(`{"type":"object"}`)}
	return stubTool{spec: spec}
}

type stubTool struct {
	spec tools.ToolSpec
}

func (t stubTool) Spec() tools.ToolSpec { return t.spec }
func (stubTool) Prepare(context.Context, json.RawMessage, tools.PrepareEnv) (*tools.PreparedCall, error) {
	return &tools.PreparedCall{ToolName: "read_file", Effects: []tools.Effect{tools.EffectRead}, CanonicalArg: json.RawMessage(`{}`), Digest: "x"}, nil
}
func (stubTool) Execute(context.Context, *tools.PreparedCall, tools.Capability, tools.ExecEnv) (*tools.ToolResult, error) {
	return &tools.ToolResult{Output: ""}, io.EOF
}

// Suppress unused-import warnings when individual tests are compiled in isolation.
var _ = context.Background
var _ = (*sync.Mutex)(nil)
