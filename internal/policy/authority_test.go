// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func TestCapabilityBindsSnapshotAndCannotReplay(t *testing.T) {
	root := testRoot(t)
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	call := testCall(t, root, []tools.Effect{tools.EffectRead}, "main.go", false)
	engine := testEngine(t, root, ModeInteractive, nil)
	authorizer, err := NewAuthorizer(AuthorizerOptions{Engine: engine, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := authorizer.Authorize(context.Background(), "run-1", root, call)
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	use := tools.CapabilityUse{RunID: "run-1", WorkDir: root, At: now}
	if err := authorizer.Verifier().Consume(context.Background(), call, capability, use); err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if err := authorizer.Verifier().Consume(context.Background(), call, capability, use); !errors.Is(err, ErrCapabilityReplay) {
		t.Fatalf("replay error = %v, want ErrCapabilityReplay", err)
	}
}

func TestCapabilityRejectsMutationWithoutBurningGrant(t *testing.T) {
	root := testRoot(t)
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	call := testCall(t, root, []tools.Effect{tools.EffectRead}, "main.go", false)
	engine := testEngine(t, root, ModeInteractive, nil)
	authorizer, _ := NewAuthorizer(AuthorizerOptions{Engine: engine, Now: func() time.Time { return now }})
	capability, _ := authorizer.Authorize(context.Background(), "run-1", root, call)
	use := tools.CapabilityUse{RunID: "run-1", WorkDir: root, At: now}

	mutated := *call
	mutated.ID = "call-2"
	if err := authorizer.Verifier().Consume(context.Background(), &mutated, capability, use); !errors.Is(err, tools.ErrCapabilityMismatch) {
		t.Fatalf("mutated call error = %v", err)
	}
	badCapability := capability
	badCapability.Paths = []string{filepath.Join(root, "other.go")}
	if err := authorizer.Verifier().Consume(context.Background(), call, badCapability, use); !errors.Is(err, tools.ErrCapabilityMismatch) {
		t.Fatalf("mutated capability error = %v", err)
	}
	if err := authorizer.Verifier().Consume(context.Background(), call, capability, use); err != nil {
		t.Fatalf("valid grant was burned by mutation: %v", err)
	}
}

func TestCapabilityBindsEverySecurityField(t *testing.T) {
	root := testRoot(t)
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	base := testCall(t, root, []tools.Effect{tools.EffectRead}, "main.go", false)
	base.Preconditions = []tools.Precondition{{Kind: tools.PreconditionFileSHA256, Path: base.Paths[0].Path, SHA256: "hash-1"}}
	engine := testEngine(t, root, ModeInteractive, nil)

	tests := map[string]func(*tools.PreparedCall, *tools.Capability, *tools.CapabilityUse){
		"use run id": func(_ *tools.PreparedCall, _ *tools.Capability, use *tools.CapabilityUse) { use.RunID = "run-2" },
		"use workdir": func(_ *tools.PreparedCall, _ *tools.Capability, use *tools.CapabilityUse) {
			use.WorkDir = filepath.Join(root, "sub")
		},
		"cap run id": func(_ *tools.PreparedCall, cap *tools.Capability, _ *tools.CapabilityUse) { cap.RunID = "run-2" },
		"cap workdir": func(_ *tools.PreparedCall, cap *tools.Capability, _ *tools.CapabilityUse) {
			cap.WorkDir = filepath.Join(root, "sub")
		},
		"cap tool":   func(_ *tools.PreparedCall, cap *tools.Capability, _ *tools.CapabilityUse) { cap.ToolName = "other" },
		"cap digest": func(_ *tools.PreparedCall, cap *tools.Capability, _ *tools.CapabilityUse) { cap.Digest = "other" },
		"cap paths": func(_ *tools.PreparedCall, cap *tools.Capability, _ *tools.CapabilityUse) {
			cap.Paths[0] = filepath.Join(root, "other.go")
		},
		"cap effects": func(_ *tools.PreparedCall, cap *tools.Capability, _ *tools.CapabilityUse) {
			cap.Effects[0] = tools.EffectWrite
		},
		"cap expiry": func(_ *tools.PreparedCall, cap *tools.Capability, _ *tools.CapabilityUse) {
			cap.ExpiresAt = cap.ExpiresAt.Add(time.Second)
		},
		"call id":   func(call *tools.PreparedCall, _ *tools.Capability, _ *tools.CapabilityUse) { call.ID = "call-2" },
		"call tool": func(call *tools.PreparedCall, _ *tools.Capability, _ *tools.CapabilityUse) { call.ToolName = "other" },
		"call args": func(call *tools.PreparedCall, _ *tools.Capability, _ *tools.CapabilityUse) {
			call.CanonicalArg = []byte(`{"value":2}`)
		},
		"call digest": func(call *tools.PreparedCall, _ *tools.Capability, _ *tools.CapabilityUse) { call.Digest = "other" },
		"call effects": func(call *tools.PreparedCall, _ *tools.Capability, _ *tools.CapabilityUse) {
			call.Effects[0] = tools.EffectWrite
		},
		"call path": func(call *tools.PreparedCall, _ *tools.Capability, _ *tools.CapabilityUse) {
			call.Paths[0].Path = filepath.Join(root, "other.go")
		},
		"path sensitivity": func(call *tools.PreparedCall, _ *tools.Capability, _ *tools.CapabilityUse) {
			call.Paths[0].Sensitive = true
		},
		"precondition": func(call *tools.PreparedCall, _ *tools.Capability, _ *tools.CapabilityUse) {
			call.Preconditions[0].SHA256 = "hash-2"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			call := clonePreparedCall(base)
			authorizer, _ := NewAuthorizer(AuthorizerOptions{Engine: engine, Now: func() time.Time { return now }})
			capability, err := authorizer.Authorize(context.Background(), "run-1", root, call)
			if err != nil {
				t.Fatal(err)
			}
			capability.Paths = append([]string(nil), capability.Paths...)
			capability.Effects = append([]tools.Effect(nil), capability.Effects...)
			use := tools.CapabilityUse{RunID: "run-1", WorkDir: root, At: now}
			mutate(call, &capability, &use)
			if err := authorizer.Verifier().Consume(context.Background(), call, capability, use); !errors.Is(err, tools.ErrCapabilityMismatch) {
				t.Fatalf("Consume() error = %v", err)
			}
		})
	}
}

func TestCapabilityExpiryAndForgery(t *testing.T) {
	root := testRoot(t)
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	call := testCall(t, root, []tools.Effect{tools.EffectRead}, "main.go", false)
	authorizer, _ := NewAuthorizer(AuthorizerOptions{
		Engine: testEngine(t, root, ModeInteractive, nil), Now: func() time.Time { return now }, CapabilityTTL: time.Second,
	})
	capability, _ := authorizer.Authorize(context.Background(), "run-1", root, call)
	forged := capability
	forged.Nonce = "forged"
	if err := authorizer.Verifier().Consume(context.Background(), call, forged, tools.CapabilityUse{RunID: "run-1", WorkDir: root, At: now}); !errors.Is(err, ErrCapabilityUnknown) {
		t.Fatalf("forged error = %v", err)
	}
	if err := authorizer.Verifier().Consume(context.Background(), call, capability, tools.CapabilityUse{RunID: "run-1", WorkDir: root, At: now.Add(time.Second)}); !errors.Is(err, ErrCapabilityExpired) {
		t.Fatalf("expired error = %v", err)
	}
}

func TestCapabilityConcurrentConsumeHasSingleWinner(t *testing.T) {
	root := testRoot(t)
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	call := testCall(t, root, []tools.Effect{tools.EffectRead}, "main.go", false)
	authorizer, _ := NewAuthorizer(AuthorizerOptions{Engine: testEngine(t, root, ModeInteractive, nil), Now: func() time.Time { return now }})
	capability, _ := authorizer.Authorize(context.Background(), "run-1", root, call)
	use := tools.CapabilityUse{RunID: "run-1", WorkDir: root, At: now}

	var successes atomic.Int32
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if authorizer.Verifier().Consume(context.Background(), call, capability, use) == nil {
				successes.Add(1)
			}
		}()
	}
	wait.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful consumes = %d, want 1", got)
	}
}
