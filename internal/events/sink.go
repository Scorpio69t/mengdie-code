// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Sink consumes validated events. Implementations must return write failures to
// the caller instead of silently dropping events.
type Sink interface {
	Emit(context.Context, Event) error
}

// Emitter serializes event creation for one run and assigns monotonic sequence
// numbers in the same order events reach the Sink.
type Emitter struct {
	mu    sync.Mutex
	runID string
	seq   uint64
	now   func() time.Time
	sink  Sink
}

// NewEmitter constructs a run-scoped emitter. The clock is injectable so tests
// never need sleeps; production callers normally pass time.Now.
func NewEmitter(runID string, sink Sink, now func() time.Time) (*Emitter, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errors.New("emitter run_id is required")
	}
	if sink == nil {
		return nil, errors.New("emitter sink is required")
	}
	if now == nil {
		return nil, errors.New("emitter clock is required")
	}
	return &Emitter{runID: runID, sink: sink, now: now}, nil
}

// Emit constructs, validates and synchronously sends one event.
func (e *Emitter) Emit(ctx context.Context, kind Kind, payload any) (Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	e.seq++
	event, err := New(e.runID, e.seq, e.now(), kind, payload)
	if err != nil {
		return Event{}, err
	}
	if err := e.sink.Emit(ctx, event); err != nil {
		return event, err
	}
	return event, nil
}

// MemorySink stores events for tests and M1 in-process consumers.
type MemorySink struct {
	mu     sync.RWMutex
	events []Event
}

func (s *MemorySink) Emit(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	copyEvent := cloneEvent(event)
	s.mu.Lock()
	s.events = append(s.events, copyEvent)
	s.mu.Unlock()
	return nil
}

// Events returns a defensive snapshot in emission order.
func (s *MemorySink) Events() []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Event, len(s.events))
	for i, event := range s.events {
		result[i] = cloneEvent(event)
	}
	return result
}

func cloneEvent(event Event) Event {
	event.Payload = append(event.Payload[:0:0], event.Payload...)
	return event
}
