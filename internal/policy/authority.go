// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

var (
	ErrCapabilityUnknown = errors.New("capability is unknown")
	ErrCapabilityExpired = errors.New("capability has expired")
	ErrCapabilityReplay  = errors.New("capability has already been used")
)

type authority struct {
	mu     sync.Mutex
	ttl    time.Duration
	grants map[string]grant
	used   map[string]time.Time
}

type grant struct {
	runID         string
	workDir       string
	callID        string
	toolName      string
	digest        string
	canonicalArg  string
	effects       []tools.Effect
	paths         []tools.PathResource
	preconditions []tools.Precondition
	expiresAt     time.Time
}

func newAuthority(ttl time.Duration) (*authority, error) {
	if ttl <= 0 {
		return nil, errors.New("policy: capability TTL must be positive")
	}
	return &authority{ttl: ttl, grants: make(map[string]grant), used: make(map[string]time.Time)}, nil
}

func (a *authority) issue(runID, workDir string, call *tools.PreparedCall, now time.Time) (tools.Capability, error) {
	if runID == "" || workDir == "" || now.IsZero() {
		return tools.Capability{}, errors.New("policy: run_id, workdir and time are required")
	}
	if err := call.Validate(); err != nil {
		return tools.Capability{}, fmt.Errorf("policy: invalid prepared call: %w", err)
	}
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return tools.Capability{}, fmt.Errorf("policy: generate capability nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	expiresAt := now.Add(a.ttl).UTC()
	paths := make([]string, len(call.Paths))
	for index, resource := range call.Paths {
		paths[index] = resource.Path
	}
	capability := tools.Capability{
		RunID:     runID,
		ToolName:  call.ToolName,
		Digest:    call.Digest,
		WorkDir:   workDir,
		Paths:     paths,
		Effects:   append([]tools.Effect(nil), call.Effects...),
		ExpiresAt: expiresAt,
		Nonce:     nonce,
	}
	a.mu.Lock()
	a.cleanup(now)
	a.grants[nonce] = snapshotGrant(runID, workDir, call, expiresAt)
	a.mu.Unlock()
	return capability, nil
}

func snapshotGrant(runID, workDir string, call *tools.PreparedCall, expiresAt time.Time) grant {
	return grant{
		runID:         runID,
		workDir:       workDir,
		callID:        call.ID,
		toolName:      call.ToolName,
		digest:        call.Digest,
		canonicalArg:  string(call.CanonicalArg),
		effects:       append([]tools.Effect(nil), call.Effects...),
		paths:         append([]tools.PathResource(nil), call.Paths...),
		preconditions: append([]tools.Precondition(nil), call.Preconditions...),
		expiresAt:     expiresAt,
	}
}

// Consume implements tools.CapabilityVerifier. The successful transition from
// unused to used is atomic, so concurrent executions cannot both proceed.
func (a *authority) Consume(ctx context.Context, call *tools.PreparedCall, capability tools.Capability, use tools.CapabilityUse) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if call == nil {
		return tools.ErrCapabilityMismatch
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleanup(use.At)
	if _, replay := a.used[capability.Nonce]; replay {
		return ErrCapabilityReplay
	}
	stored, ok := a.grants[capability.Nonce]
	if !ok {
		return ErrCapabilityUnknown
	}
	if !use.At.Before(stored.expiresAt) {
		delete(a.grants, capability.Nonce)
		a.used[capability.Nonce] = stored.expiresAt
		return ErrCapabilityExpired
	}
	if !grantMatches(stored, call, capability, use) {
		return tools.ErrCapabilityMismatch
	}
	delete(a.grants, capability.Nonce)
	a.used[capability.Nonce] = stored.expiresAt
	return nil
}

func grantMatches(stored grant, call *tools.PreparedCall, capability tools.Capability, use tools.CapabilityUse) bool {
	if stored.runID != use.RunID || stored.runID != capability.RunID ||
		stored.workDir != use.WorkDir || stored.workDir != capability.WorkDir ||
		stored.callID != call.ID || stored.toolName != call.ToolName || stored.toolName != capability.ToolName ||
		stored.digest != call.Digest || stored.digest != capability.Digest ||
		stored.canonicalArg != string(call.CanonicalArg) || !stored.expiresAt.Equal(capability.ExpiresAt) {
		return false
	}
	if !slices.Equal(stored.effects, call.Effects) || !slices.Equal(stored.effects, capability.Effects) ||
		!slices.Equal(stored.paths, call.Paths) || !slices.Equal(stored.preconditions, call.Preconditions) {
		return false
	}
	paths := make([]string, len(stored.paths))
	for index, resource := range stored.paths {
		paths[index] = resource.Path
	}
	return slices.Equal(paths, capability.Paths)
}

func (a *authority) cleanup(now time.Time) {
	for nonce, expiresAt := range a.used {
		if !now.Before(expiresAt) {
			delete(a.used, nonce)
		}
	}
}
