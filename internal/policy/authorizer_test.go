// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

type fixedBroker struct {
	response ApprovalResponse
	err      error
	calls    int
}

func (b *fixedBroker) Decide(_ context.Context, _ ApprovalRequest) (ApprovalResponse, error) {
	b.calls++
	return b.response, b.err
}

type failingObserver struct{ err error }

func (o failingObserver) Needed(context.Context, ApprovalRequest) error { return o.err }
func (o failingObserver) Resolved(context.Context, ApprovalRequest, ApprovalResponse) error {
	return o.err
}

type resolvedFailObserver struct{ err error }

func (resolvedFailObserver) Needed(context.Context, ApprovalRequest) error { return nil }
func (o resolvedFailObserver) Resolved(context.Context, ApprovalRequest, ApprovalResponse) error {
	return o.err
}

func TestAuthorizerApprovalOutcomes(t *testing.T) {
	root := testRoot(t)
	call := testCall(t, root, []tools.Effect{tools.EffectWrite}, "main.go", false)
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		choice ApprovalChoice
		want   error
	}{
		{"approve", ApprovalApprove, nil},
		{"reject", ApprovalReject, ErrDenied},
		{"edit", ApprovalEdit, ErrReprepare},
	} {
		t.Run(test.name, func(t *testing.T) {
			broker := &fixedBroker{response: ApprovalResponse{Choice: test.choice}}
			authorizer, _ := NewAuthorizer(AuthorizerOptions{
				Engine: testEngine(t, root, ModeInteractive, nil), Broker: broker, Now: func() time.Time { return now },
			})
			capability, err := authorizer.Authorize(context.Background(), "run-1", root, call)
			if !errors.Is(err, test.want) {
				t.Fatalf("Authorize() error = %v, want %v", err, test.want)
			}
			if test.want != nil && capability.Nonce != "" {
				t.Fatal("rejected or edited call received capability")
			}
			if broker.calls != 1 {
				t.Fatalf("broker calls = %d", broker.calls)
			}
		})
	}
}

func TestHeadlessAskBecomesDenyWithoutBroker(t *testing.T) {
	root := testRoot(t)
	broker := &fixedBroker{response: ApprovalResponse{Choice: ApprovalApprove}}
	authorizer, _ := NewAuthorizer(AuthorizerOptions{Engine: testEngine(t, root, ModeHeadless, func(options *Options) {
		options.CLI = []Rule{{Name: "ask-write", Effects: []tools.Effect{tools.EffectWrite}, Decision: DecisionAsk}}
	}), Broker: broker})
	_, err := authorizer.Authorize(context.Background(), "run-1", root, testCall(t, root, []tools.Effect{tools.EffectWrite}, "main.go", false))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("Authorize() error = %v, want ErrDenied", err)
	}
	if broker.calls != 0 {
		t.Fatalf("headless broker calls = %d, want 0", broker.calls)
	}
}

func TestObserverFailurePreventsCapabilityIssuance(t *testing.T) {
	root := testRoot(t)
	observerErr := errors.New("sink failed")
	broker := &fixedBroker{response: ApprovalResponse{Choice: ApprovalApprove}}
	authorizer, _ := NewAuthorizer(AuthorizerOptions{
		Engine: testEngine(t, root, ModeInteractive, nil), Broker: broker, Observer: failingObserver{err: observerErr},
	})
	capability, err := authorizer.Authorize(context.Background(), "run-1", root, testCall(t, root, []tools.Effect{tools.EffectWrite}, "main.go", false))
	if !errors.Is(err, observerErr) || capability.Nonce != "" || broker.calls != 0 {
		t.Fatalf("capability=%+v error=%v broker_calls=%d", capability, err, broker.calls)
	}
}

func TestResolvedEventFailurePreventsCapabilityIssuance(t *testing.T) {
	root := testRoot(t)
	observerErr := errors.New("sink failed")
	broker := &fixedBroker{response: ApprovalResponse{Choice: ApprovalApprove}}
	authorizer, _ := NewAuthorizer(AuthorizerOptions{
		Engine: testEngine(t, root, ModeInteractive, nil), Broker: broker, Observer: resolvedFailObserver{err: observerErr},
	})
	capability, err := authorizer.Authorize(context.Background(), "run-1", root, testCall(t, root, []tools.Effect{tools.EffectWrite}, "main.go", false))
	if !errors.Is(err, observerErr) || capability.Nonce != "" || broker.calls != 1 {
		t.Fatalf("capability=%+v error=%v broker_calls=%d", capability, err, broker.calls)
	}
}

func TestEventObserverEmitsBoundedNonSensitivePayloads(t *testing.T) {
	root := testRoot(t)
	sink := &events.MemorySink{}
	emitter, err := events.NewEmitter("run-1", sink, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	broker := &fixedBroker{response: ApprovalResponse{Choice: ApprovalReject, Reason: "不允许"}}
	authorizer, _ := NewAuthorizer(AuthorizerOptions{
		Engine: testEngine(t, root, ModeInteractive, nil), Broker: broker, Observer: EventObserver{Emitter: emitter},
	})
	call := testCall(t, root, []tools.Effect{tools.EffectWrite}, "secret.txt", false)
	call.CanonicalArg = json.RawMessage(`{"secret":"DO_NOT_LEAK"}`)
	call.Digest = tools.ComputeDigest(call.ToolName, call.CanonicalArg)
	_, _ = authorizer.Authorize(context.Background(), "run-1", root, call)

	got := sink.Events()
	if len(got) != 2 || got[0].Kind != events.KindApprovalNeeded || got[1].Kind != events.KindApprovalResolved {
		t.Fatalf("events = %+v", got)
	}
	for _, event := range got {
		if strings.Contains(string(event.Payload), "DO_NOT_LEAK") || strings.Contains(string(event.Payload), "secret.txt") {
			t.Fatalf("sensitive details leaked in event: %s", event.Payload)
		}
	}
}

func TestReadToolRequiresAuthorizedOneShotCapability(t *testing.T) {
	root := testRoot(t)
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	guard, err := platform.NewPathGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	tool := tools.NewReadFile()
	call, err := tool.Prepare(context.Background(), json.RawMessage(`{"path":"main.go"}`), tools.PrepareEnv{CallID: "call-1", Guard: guard})
	if err != nil {
		t.Fatal(err)
	}
	authorizer, _ := NewAuthorizer(AuthorizerOptions{Engine: testEngine(t, root, ModeInteractive, nil)})
	capability, err := authorizer.Authorize(context.Background(), "run-1", root, call)
	if err != nil {
		t.Fatal(err)
	}
	env := tools.ExecEnv{RunID: "run-1", Guard: guard, CapabilityVerifier: authorizer.Verifier()}
	if _, err := tool.Execute(context.Background(), call, capability, env); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := tool.Execute(context.Background(), call, capability, env); !errors.Is(err, ErrCapabilityReplay) {
		t.Fatalf("replayed Execute() error = %v", err)
	}
}

func TestDeniedCallHasZeroSideEffects(t *testing.T) {
	root := testRoot(t)
	call := testCall(t, root, []tools.Effect{tools.EffectWrite}, "main.go", false)
	authorizer, _ := NewAuthorizer(AuthorizerOptions{Engine: testEngine(t, root, ModeHeadless, nil)})
	capability, err := authorizer.Authorize(context.Background(), "run-1", root, call)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("Authorize() error = %v", err)
	}

	sideEffects := 0
	execute := func() error {
		env := tools.ExecEnv{RunID: "run-1", CapabilityVerifier: authorizer.Verifier()}
		if err := tools.CheckCapability(context.Background(), call, capability, env); err != nil {
			return err
		}
		sideEffects++
		return nil
	}
	if err := execute(); err == nil {
		t.Fatal("denied call executed")
	}
	if sideEffects != 0 {
		t.Fatalf("side effects = %d, want 0", sideEffects)
	}
}

func TestAuthorizerCanonicalizesWorkDirAlias(t *testing.T) {
	rawRoot := t.TempDir()
	guard, err := platform.NewPathGuard(rawRoot)
	if err != nil {
		t.Fatal(err)
	}
	engine := testEngine(t, rawRoot, ModeInteractive, nil)
	authorizer, err := NewAuthorizer(AuthorizerOptions{Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	alias := rawRoot + string(filepath.Separator) + "."
	call := testCall(t, guard.Root(), []tools.Effect{tools.EffectRead}, "main.go", false)
	capability, err := authorizer.Authorize(context.Background(), "run-1", alias, call)
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if capability.WorkDir != engine.root {
		t.Fatalf("capability workdir = %q, want canonical root %q", capability.WorkDir, engine.root)
	}
}

func TestAuthorizerRejectsDifferentWorkDirBeforeApproval(t *testing.T) {
	root := testRoot(t)
	broker := &fixedBroker{response: ApprovalResponse{Choice: ApprovalApprove}}
	authorizer, err := NewAuthorizer(AuthorizerOptions{
		Engine: testEngine(t, root, ModeInteractive, nil), Broker: broker,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = authorizer.Authorize(
		context.Background(), "run-1", testRoot(t),
		testCall(t, root, []tools.Effect{tools.EffectWrite}, "main.go", false),
	)
	if !errors.Is(err, ErrWorkDirMismatch) {
		t.Fatalf("Authorize() error = %v, want ErrWorkDirMismatch", err)
	}
	if broker.calls != 0 {
		t.Fatalf("broker calls = %d, want 0", broker.calls)
	}
}
