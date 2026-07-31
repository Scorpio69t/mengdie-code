// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package tools defines the tool protocol shared by every concrete tool:
// side-effect declaration, side-effect-free Prepare, authorized Execute,
// canonical argument digests and preconditions. Concrete tools
// (read/list/search in P1-05, edit/write in P1-07, shell in P1-08) plug
// into this boundary; Policy and Approval (P1-06) arbitrate between the
// two phases.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

// Effect declares the side-effect classes a tool call can produce. Policy
// decisions, execution serialization and UI risk display all derive from
// this declaration; a tool must never produce effects it did not declare.
type Effect string

const (
	EffectRead    Effect = "read"
	EffectWrite   Effect = "write"
	EffectExecute Effect = "execute"
	EffectNetwork Effect = "network"
)

// ToolSpec is the static contract of a tool, suitable for provider tool
// schemas and Policy default rules.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Effects     []Effect        `json:"effects"`
}

// Tool is the two-phase execution boundary. Prepare validates and
// canonicalizes arguments, computes the preview, preconditions and digest,
// and must not produce any external side effect. Execute runs only after
// Policy and Approval have authorized the exact PreparedCall, carrying the
// one-shot Capability that binds the approval.
type Tool interface {
	Spec() ToolSpec
	Prepare(ctx context.Context, raw json.RawMessage, env PrepareEnv) (*PreparedCall, error)
	Execute(ctx context.Context, call *PreparedCall, cap Capability, env ExecEnv) (*ToolResult, error)
}

// PrepareEnv carries the read-only context a tool may consult during
// Prepare. It grants no execution power.
type PrepareEnv struct {
	// Guard enforces the project-root boundary for every path the tool
	// touches. Tools must route all user/model paths through it.
	Guard *platform.PathGuard
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

// ExecEnv carries the execution-time context. Concrete fields (process
// handles, output limits) arrive with the tools that need them.
type ExecEnv struct {
	Now func() time.Time
}

// PreviewKind classifies what Approval should display.
type PreviewKind string

const (
	PreviewNone    PreviewKind = "none"
	PreviewRead    PreviewKind = "read"
	PreviewDiff    PreviewKind = "diff"
	PreviewCommand PreviewKind = "command"
)

// Preview is the bounded, human-reviewable description of a call. The body
// is produced by the tool and must already respect output limits.
type Preview struct {
	Kind  PreviewKind
	Title string
	Body  string
}

// PreconditionKind identifies a verifiable precondition.
type PreconditionKind string

const (
	// PreconditionFileSHA256 requires the file at Path to still hash to
	// SHA256 immediately before Execute.
	PreconditionFileSHA256 PreconditionKind = "file_sha256"
)

// Precondition binds Execute to state observed during Prepare. A failed
// precondition means the world changed after approval (TOCTOU) and the
// call must fail safely instead of acting on stale assumptions.
type Precondition struct {
	Kind   PreconditionKind
	Path   string
	SHA256 string
}

// PreparedCall is the canonical, approvable form of a tool call. Approval
// binds Digest; Execute re-verifies Preconditions before acting.
type PreparedCall struct {
	ID            string
	ToolName      string
	CanonicalArg  json.RawMessage
	Effects       []Effect
	Preview       Preview
	Preconditions []Precondition
	Digest        string
}

// ToolResult is the bounded outcome returned to the Agent loop.
type ToolResult struct {
	Output    string
	Truncated bool
	Metadata  map[string]string
}

// Capability is the one-shot token minted by Approval (P1-06) after a
// successful policy decision. It binds the approval to one exact call:
// approving `git status` can never be replayed as another command.
// Minting and verification belong to P1-06; the zero value means "no
// capability granted" and must be rejected by Execute.
type Capability struct {
	RunID     string
	ToolName  string
	Digest    string
	WorkDir   string
	Paths     []string
	Effects   []Effect
	ExpiresAt time.Time
	Nonce     string
}

// ErrCapabilityMissing rejects Execute calls that arrive without a
// capability for side-effecting tools.
var ErrCapabilityMissing = errors.New("capability missing")

// Validate performs the protocol-level checks every PreparedCall must pass
// regardless of the producing tool.
func (c *PreparedCall) Validate() error {
	if c.ID == "" {
		return errors.New("prepared call: empty id")
	}
	if c.ToolName == "" {
		return errors.New("prepared call: empty tool name")
	}
	if len(c.CanonicalArg) == 0 || !json.Valid(c.CanonicalArg) {
		return errors.New("prepared call: canonical argument is not valid JSON")
	}
	if len(c.Effects) == 0 {
		return fmt.Errorf("prepared call: %s declares no effects", c.ToolName)
	}
	seen := make(map[Effect]struct{}, len(c.Effects))
	for _, effect := range c.Effects {
		switch effect {
		case EffectRead, EffectWrite, EffectExecute, EffectNetwork:
		default:
			return fmt.Errorf("prepared call: unknown effect %q", effect)
		}
		if _, dup := seen[effect]; dup {
			return fmt.Errorf("prepared call: duplicate effect %q", effect)
		}
		seen[effect] = struct{}{}
	}
	for _, precondition := range c.Preconditions {
		if precondition.Kind != PreconditionFileSHA256 {
			return fmt.Errorf("prepared call: unknown precondition kind %q", precondition.Kind)
		}
		if precondition.Path == "" || precondition.SHA256 == "" {
			return errors.New("prepared call: file_sha256 precondition requires path and hash")
		}
	}
	if c.Digest == "" {
		return errors.New("prepared call: empty digest")
	}
	if c.Digest != ComputeDigest(c.ToolName, c.CanonicalArg) {
		return errors.New("prepared call: digest does not match tool name and canonical argument")
	}
	return nil
}
