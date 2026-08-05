// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

const defaultCapabilityTTL = 30 * time.Second

// ApprovalChoice is a human response to one exact prepared call.
type ApprovalChoice string

const (
	ApprovalApprove ApprovalChoice = "approve"
	ApprovalReject  ApprovalChoice = "reject"
	ApprovalEdit    ApprovalChoice = "edit"
)

// ApprovalRequest is deliberately bounded and excludes arguments, source
// text and diffs. Rich previews remain in the local UI layer.
type ApprovalRequest struct {
	CallID  string
	Tool    string
	Prompt  string
	Risk    string
	Effects []tools.Effect
}

type ApprovalResponse struct {
	Choice ApprovalChoice
	Reason string
}

type Broker interface {
	Decide(context.Context, ApprovalRequest) (ApprovalResponse, error)
}

type Observer interface {
	Needed(context.Context, ApprovalRequest) error
	Resolved(context.Context, ApprovalRequest, ApprovalResponse) error
}

type nopObserver struct{}

func (nopObserver) Needed(context.Context, ApprovalRequest) error { return nil }
func (nopObserver) Resolved(context.Context, ApprovalRequest, ApprovalResponse) error {
	return nil
}

type AuthorizerOptions struct {
	Engine        *Engine
	Broker        Broker
	Observer      Observer
	Now           func() time.Time
	CapabilityTTL time.Duration
}

// Authorizer is run-safe and may be shared by concurrent tool calls.
type Authorizer struct {
	engine    *Engine
	broker    Broker
	observer  Observer
	now       func() time.Time
	authority *authority
}

func NewAuthorizer(options AuthorizerOptions) (*Authorizer, error) {
	if options.Engine == nil {
		return nil, errors.New("policy: engine is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Observer == nil {
		options.Observer = nopObserver{}
	}
	if options.CapabilityTTL == 0 {
		options.CapabilityTTL = defaultCapabilityTTL
	}
	authority, err := newAuthority(options.CapabilityTTL)
	if err != nil {
		return nil, err
	}
	return &Authorizer{
		engine: options.Engine, broker: options.Broker, observer: options.Observer,
		now: options.Now, authority: authority,
	}, nil
}

// Authorize evaluates one exact prepared call and returns the only capability
// verifier that can consume the grant. An edited call is never authorized: it
// must go through Prepare and policy evaluation again.
func (a *Authorizer) Authorize(ctx context.Context, runID, workDir string, call *tools.PreparedCall) (tools.Capability, error) {
	if err := ctx.Err(); err != nil {
		return tools.Capability{}, err
	}
	call = clonePreparedCall(call)
	result := a.engine.Evaluate(call)
	switch result.Decision {
	case DecisionDeny:
		return tools.Capability{}, fmt.Errorf("%w: %s", ErrDenied, result.Reason)
	case DecisionAllow:
		return a.authority.issue(runID, workDir, call, a.now())
	case DecisionAsk:
		if a.engine.Mode() == ModeHeadless || a.broker == nil {
			return tools.Capability{}, ErrApprovalMissing
		}
	default:
		return tools.Capability{}, fmt.Errorf("policy: invalid decision %q", result.Decision)
	}

	request := makeApprovalRequest(call, result)
	if err := a.observer.Needed(ctx, request); err != nil {
		return tools.Capability{}, fmt.Errorf("emit approval needed: %w", err)
	}
	response, err := a.broker.Decide(ctx, request)
	if err != nil {
		return tools.Capability{}, fmt.Errorf("approval: %w", err)
	}
	if err := validateApprovalResponse(response); err != nil {
		return tools.Capability{}, err
	}
	if err := a.observer.Resolved(ctx, request, response); err != nil {
		return tools.Capability{}, fmt.Errorf("emit approval resolved: %w", err)
	}
	switch response.Choice {
	case ApprovalApprove:
		return a.authority.issue(runID, workDir, call, a.now())
	case ApprovalEdit:
		return tools.Capability{}, ErrReprepare
	default:
		reason := strings.TrimSpace(response.Reason)
		if reason == "" {
			reason = "用户拒绝"
		}
		return tools.Capability{}, fmt.Errorf("%w: %s", ErrDenied, reason)
	}
}

func makeApprovalRequest(call *tools.PreparedCall, result Result) ApprovalRequest {
	risk := "中"
	if hasEffect(call.Effects, tools.EffectExecute) || hasEffect(call.Effects, tools.EffectNetwork) {
		risk = "高"
	} else if onlyEffect(call.Effects, tools.EffectRead) {
		risk = "低"
	}
	return ApprovalRequest{
		CallID: call.ID, Tool: call.ToolName,
		Prompt: "是否允许执行 " + call.ToolName + "？", Risk: risk,
		Effects: append([]tools.Effect(nil), call.Effects...),
	}
}

func validateApprovalResponse(response ApprovalResponse) error {
	if len(response.Reason) > 1024 {
		return errors.New("policy: approval reason exceeds size limit")
	}
	switch response.Choice {
	case ApprovalApprove, ApprovalReject, ApprovalEdit:
		return nil
	default:
		return fmt.Errorf("policy: unsupported approval choice %q", response.Choice)
	}
}

func clonePreparedCall(call *tools.PreparedCall) *tools.PreparedCall {
	if call == nil {
		return nil
	}
	copyCall := *call
	copyCall.CanonicalArg = append([]byte(nil), call.CanonicalArg...)
	copyCall.Effects = append([]tools.Effect(nil), call.Effects...)
	copyCall.Paths = append([]tools.PathResource(nil), call.Paths...)
	copyCall.Preconditions = append([]tools.Precondition(nil), call.Preconditions...)
	return &copyCall
}

// EventObserver adapts policy decisions to the UI-independent event stream.
type EventObserver struct{ Emitter *events.Emitter }

func (o EventObserver) Needed(ctx context.Context, request ApprovalRequest) error {
	if o.Emitter == nil {
		return errors.New("policy: event emitter is required")
	}
	_, err := o.Emitter.Emit(ctx, events.KindApprovalNeeded, events.ApprovalNeeded{
		CallID: request.CallID, Prompt: request.Prompt, Risk: request.Risk,
	})
	return err
}

func (o EventObserver) Resolved(ctx context.Context, request ApprovalRequest, response ApprovalResponse) error {
	if o.Emitter == nil {
		return errors.New("policy: event emitter is required")
	}
	_, err := o.Emitter.Emit(ctx, events.KindApprovalResolved, events.ApprovalResolved{
		CallID: request.CallID, Decision: string(response.Choice), Reason: response.Reason,
	})
	return err
}

// Verifier returns the execution-boundary verifier. Callers cannot issue
// capabilities through it.
func (a *Authorizer) Verifier() tools.CapabilityVerifier { return a.authority }
