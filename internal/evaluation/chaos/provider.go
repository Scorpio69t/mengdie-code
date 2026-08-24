// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package chaos

import (
	"context"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

// Provider wraps provider.Provider so the agent runtime can be exercised
// against chaos hooks without touching production code paths.
//
// The wrap distinguishes context-summary streams from agent streams via
// IsSummary: tests may install a callback to override the default heuristic.
// When IsSummary returns true, Stream fires HookContextSummary.
//
// PreStream is invoked before MaybeFire so callers can arm hooks based on
// the request shape (for example, force the next summary-shaped stream to
// fire by calling ctrl.Arm). When PreStream is non-nil and IsSummary is the
// default heuristic, the default can be disabled by leaving IsSummary nil.
type Provider struct {
	inner     provider.Provider
	ctrl      *Controller
	IsSummary func(provider.ChatRequest) bool
	PreStream func(context.Context, provider.ChatRequest) error
}

// NewProvider wraps a provider.Provider. The inner provider must not be nil.
func NewProvider(inner provider.Provider, ctrl *Controller) *Provider {
	if inner == nil {
		panic("chaos: provider inner is nil")
	}
	if ctrl == nil {
		panic("chaos: provider controller is nil")
	}
	return &Provider{inner: inner, ctrl: ctrl, IsSummary: defaultIsSummary}
}

func defaultIsSummary(req provider.ChatRequest) bool {
	if len(req.Messages) != 2 {
		return false
	}
	if req.Messages[0].Role != provider.RoleSystem || req.Messages[1].Role != provider.RoleUser {
		return false
	}
	if len(req.Tools) != 0 {
		return false
	}
	return true
}

// ID delegates to the wrapped provider.
func (p *Provider) ID() string { return p.inner.ID() }

// Capabilities delegates to the wrapped provider.
func (p *Provider) Capabilities(ctx context.Context, model string) (provider.Capabilities, error) {
	return p.inner.Capabilities(ctx, model)
}

// Stream forwards the call unless the schedule (or an armed fire) requests
// a chaos intervention. Stream summaries fire HookContextSummary.
func (p *Provider) Stream(ctx context.Context, req provider.ChatRequest, sink provider.StreamSink) (*provider.ChatResponse, error) {
	if p.PreStream != nil {
		if err := p.PreStream(ctx, req); err != nil {
			return nil, err
		}
	}
	if p.IsSummary == nil || !p.IsSummary(req) {
		return p.inner.Stream(ctx, req, sink)
	}
	decision := p.ctrl.MaybeFire(HookContextSummary, 0, req.Model, false)
	switch decision.Fire {
	case FireAbort:
		return nil, decision.Err
	case FireContext:
		return nil, decision.Err
	case FireUnknown:
		return p.inner.Stream(ctx, req, sink)
	}
	return p.inner.Stream(ctx, req, sink)
}

// StreamSummary is a convenience for tests that want to drive the summary
// path directly without going through the runtime's budget logic. It always
// fires HookContextSummary.
func (p *Provider) StreamSummary(ctx context.Context, req provider.ChatRequest, sink provider.StreamSink) (*provider.ChatResponse, error) {
	decision := p.ctrl.MaybeFire(HookContextSummary, 0, req.Model, false)
	switch decision.Fire {
	case FireAbort:
		return nil, decision.Err
	case FireContext:
		return nil, decision.Err
	case FireUnknown:
		return p.inner.Stream(ctx, req, sink)
	}
	return p.inner.Stream(ctx, req, sink)
}
