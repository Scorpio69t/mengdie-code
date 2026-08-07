// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Scorpio69t/mengdie-code/internal/events"
)

// EventSink persists reconstructable public facts before forwarding them to
// the existing M1 renderer. Token deltas remain transient; message.completed
// is their durable reconstruction boundary.
type EventSink struct {
	mu         sync.Mutex
	store      EventStore
	downstream events.Sink
	sessionID  string
	commandID  string
	lastSeq    uint64
}

// NewEventSink constructs a store-first event adapter. afterSeq supports the
// later resume reader without changing the current one-run session behavior.
func NewEventSink(sessionID string, afterSeq uint64, store EventStore, downstream events.Sink) (*EventSink, error) {
	return newEventSink(sessionID, "", afterSeq, store, downstream)
}

// NewCommandEventSink associates every durable fact with its originating
// command while preserving the store-first broadcast guarantee.
func NewCommandEventSink(sessionID, commandID string, afterSeq uint64, store EventStore, downstream events.Sink) (*EventSink, error) {
	if commandID == "" {
		return nil, errors.New("durable event sink command id is required")
	}
	return newEventSink(sessionID, commandID, afterSeq, store, downstream)
}

func newEventSink(sessionID, commandID string, afterSeq uint64, store EventStore, downstream events.Sink) (*EventSink, error) {
	if sessionID == "" {
		return nil, errors.New("durable event sink session id is required")
	}
	if store == nil {
		return nil, errors.New("durable event sink store is required")
	}
	if downstream == nil {
		return nil, errors.New("durable event sink downstream is required")
	}
	return &EventSink{
		store: store, downstream: downstream, sessionID: sessionID,
		commandID: commandID, lastSeq: afterSeq,
	}, nil
}

// Emit commits one durable boundary before broadcasting it. A downstream
// failure therefore remains observable through Load; a store failure is never
// shown as if the fact had been committed.
func (s *EventSink) Emit(ctx context.Context, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Kind != events.KindMessageDelta {
		payload := append(json.RawMessage(nil), event.Payload...)
		if len(payload) == 0 {
			payload = json.RawMessage("null")
		}
		record := Record{
			ID:            durableEventID(event),
			SessionID:     s.sessionID,
			SessionSeq:    s.lastSeq + 1,
			RunID:         event.RunID,
			RunSeq:        event.Seq,
			CommandID:     s.commandID,
			Kind:          string(event.Kind),
			SchemaVersion: event.Version,
			Visibility:    VisibilityPublic,
			Payload:       payload,
			Time:          event.Time,
		}
		if err := s.store.Append(ctx, s.sessionID, s.lastSeq, []Record{record}); err != nil {
			return fmt.Errorf("persist %s event: %w", event.Kind, err)
		}
		s.lastSeq++
	}
	if err := s.downstream.Emit(ctx, event); err != nil {
		return fmt.Errorf("broadcast %s event after commit: %w", event.Kind, err)
	}
	return nil
}

func durableEventID(event events.Event) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", event.RunID, event.Seq, event.Version)))
	return fmt.Sprintf("evt_%x", digest[:])
}

var _ events.Sink = (*EventSink)(nil)
