// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"time"
)

// ErrCapabilityMismatch rejects a capability bound to a different tool or
// digest than the call being executed.
var ErrCapabilityMismatch = errors.New("capability does not match prepared call")

// ErrCapabilityVerifierMissing rejects a tool execution that is not wired to
// the Policy authorizer. A non-empty nonce by itself is never authority.
var ErrCapabilityVerifierMissing = errors.New("capability verifier missing")

// CapabilityUse is the execution-time binding independently observed by the
// tool boundary. WorkDir comes from PathGuard, not from model arguments.
type CapabilityUse struct {
	RunID   string
	WorkDir string
	At      time.Time
}

// CapabilityVerifier validates the complete grant and atomically consumes its
// nonce. internal/policy implements this interface; tools define it to avoid a
// dependency on the Harness package.
type CapabilityVerifier interface {
	Consume(context.Context, *PreparedCall, Capability, CapabilityUse) error
}

// CheckCapability rejects fabricated tokens locally, then delegates complete
// snapshot validation, expiry checking and atomic nonce consumption to the
// Policy authorizer before Execute can touch external state.
func CheckCapability(ctx context.Context, call *PreparedCall, cap Capability, env ExecEnv) error {
	if call == nil {
		return ErrCapabilityMismatch
	}
	if cap.Nonce == "" {
		return ErrCapabilityMissing
	}
	if cap.ToolName != call.ToolName || cap.Digest != call.Digest {
		return ErrCapabilityMismatch
	}
	if env.CapabilityVerifier == nil {
		return ErrCapabilityVerifierMissing
	}
	if env.Guard == nil {
		return errors.New("capability check requires path guard")
	}
	now := time.Now
	if env.Now != nil {
		now = env.Now
	}
	return env.CapabilityVerifier.Consume(ctx, call, cap, CapabilityUse{
		RunID:   env.RunID,
		WorkDir: env.Guard.Root(),
		At:      now(),
	})
}
