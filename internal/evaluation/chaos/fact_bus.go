// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package chaos

import "github.com/Scorpio69t/mengdie-code/internal/session"

// FactBus wraps a session.PublicFactBus so the TUI fact-gap hook can fire
// when a subscription observes a notification gap. The wrap records the
// fire and then forwards the notification unchanged so the TUI replay
// path is exercised end-to-end.
type FactBus struct {
	inner *session.PublicFactBus
	ctrl  *Controller
}

// NewFactBus wraps a session.PublicFactBus. The inner bus must not be nil.
func NewFactBus(inner *session.PublicFactBus, ctrl *Controller) *FactBus {
	if inner == nil {
		panic("chaos: fact bus inner is nil")
	}
	if ctrl == nil {
		panic("chaos: fact bus controller is nil")
	}
	return &FactBus{inner: inner, ctrl: ctrl}
}

// Subscribe delegates to the inner bus. Tests that exercise the TUI gap
// recovery flow call this; the actual fire happens in OnGap.
func (b *FactBus) Subscribe(sessionID string, afterSeq uint64) (session.PublicFactSubscription, error) {
	return b.inner.Subscribe(sessionID, afterSeq)
}

// PublishCommitted forwards the fact to the inner bus. When the bus
// reports a gap for any subscriber, OnGap fires HookTUIFactGap.
func (b *FactBus) PublishCommitted(fact session.PublicFact) {
	b.inner.PublishCommitted(fact)
}

// OnGap fires HookTUIFactGap for the given seq. Tests call this directly
// after constructing a bus without an inner, or after manipulating the
// bus to drop a notification.
func (b *FactBus) OnGap(seq uint64) {
	b.ctrl.MaybeFire(HookTUIFactGap, seq, "", false)
}
