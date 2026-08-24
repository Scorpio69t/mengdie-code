// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package chaos

import (
	"context"

	"github.com/Scorpio69t/mengdie-code/internal/events"
)

// Sink wraps an events.Sink so the EventStore commit boundary becomes a
// chaos hook. Fires are evaluated after the event has been constructed but
// before the underlying sink commits it; FireAbort therefore maps to a
// "client saw the event seq but the sink never persisted it" situation,
// which is exactly the recovery scenario EventStore Append + AfterSeq replay
// must survive.
type Sink struct {
	inner events.Sink
	ctrl  *Controller
}

// NewSink wraps an events.Sink. The inner sink must not be nil; pass the
// session EventStore adapter (or a memory sink for unit tests).
func NewSink(inner events.Sink, ctrl *Controller) *Sink {
	if inner == nil {
		panic("chaos: sink inner is nil")
	}
	if ctrl == nil {
		panic("chaos: sink controller is nil")
	}
	return &Sink{inner: inner, ctrl: ctrl}
}

// Emit forwards the event to the inner sink unless the schedule requests a
// fire on the matching seq. The returned event always reflects the seq the
// emitter assigned, so the caller sees a consistent event identifier.
func (s *Sink) Emit(ctx context.Context, event events.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	decision := s.ctrl.MaybeFire(HookEventStoreCommit, event.Seq, string(event.Kind), false)
	switch decision.Fire {
	case FireAbort:
		return decision.Err
	case FireContext:
		return decision.Err
	case FireUnknown:
		if err := s.inner.Emit(ctx, event); err != nil {
			return err
		}
		return nil
	default:
		return s.inner.Emit(ctx, event)
	}
}

// Compiled to silence unused-import when only Chaos hooks are wired.
var _ = context.Background
