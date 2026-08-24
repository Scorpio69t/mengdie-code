// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package chaos

import (
	"context"

	"github.com/Scorpio69t/mengdie-code/internal/policy"
)

// Broker wraps policy.Broker so the pending-approval boundary becomes a
// chaos hook. The wrap fires HookPendingApproval before delegating Decide
// to the inner broker; FireAbort / FireContext prevent the inner broker
// from ever being consulted, simulating a kill before the user resolved
// the prompt. FireUnknown lets the inner broker return normally while
// recording that the post-approval state should be treated as uncertain.
type Broker struct {
	inner policy.Broker
	ctrl  *Controller
}

// NewBroker wraps a policy.Broker. The inner broker must not be nil.
func NewBroker(inner policy.Broker, ctrl *Controller) *Broker {
	if inner == nil {
		panic("chaos: broker inner is nil")
	}
	if ctrl == nil {
		panic("chaos: broker controller is nil")
	}
	return &Broker{inner: inner, ctrl: ctrl}
}

// Decide is the only entry point on policy.Broker. It fires the hook first
// so tests can prove that a kill at "waiting for approval" never silently
// resolves the approval.
func (b *Broker) Decide(ctx context.Context, req policy.ApprovalRequest) (policy.ApprovalResponse, error) {
	decision := b.ctrl.MaybeFire(HookPendingApproval, 0, req.Tool, false)
	switch decision.Fire {
	case FireAbort:
		return policy.ApprovalResponse{}, decision.Err
	case FireContext:
		return policy.ApprovalResponse{}, decision.Err
	case FireUnknown:
		return b.inner.Decide(ctx, req)
	default:
		return b.inner.Decide(ctx, req)
	}
}
