// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
)

func TestEventSinkPersistsBeforeBroadcastAndSkipsDelta(t *testing.T) {
	order := []string{}
	store := &fakeEventStore{onAppend: func() { order = append(order, "store") }}
	downstream := eventSinkFunc(func(context.Context, events.Event) error {
		order = append(order, "downstream")
		return nil
	})
	sink, err := NewEventSink("session-1", 0, store, downstream)
	if err != nil {
		t.Fatal(err)
	}
	delta := testPublicEvent(t, 1, events.KindMessageDelta, events.MessageDelta{Text: "a"})
	if err := sink.Emit(context.Background(), delta); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 0 || !reflect.DeepEqual(order, []string{"downstream"}) {
		t.Fatalf("delta records=%d order=%v", len(store.records), order)
	}
	order = nil
	completed := testPublicEvent(t, 2, events.KindMessageCompleted, events.MessageCompleted{Text: "answer"})
	if err := sink.Emit(context.Background(), completed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"store", "downstream"}) {
		t.Fatalf("order=%v", order)
	}
	if len(store.records) != 1 {
		t.Fatalf("records=%d", len(store.records))
	}
	record := store.records[0]
	if record.SessionSeq != 1 || record.RunSeq != 2 || record.Kind != string(events.KindMessageCompleted) || record.ID == "" {
		t.Fatalf("record=%+v", record)
	}
}

func TestEventSinkFailureSemantics(t *testing.T) {
	wantStore := errors.New("database failed")
	wantRenderer := errors.New("renderer failed")
	t.Run("store failure is not broadcast", func(t *testing.T) {
		broadcast := false
		store := &fakeEventStore{err: wantStore}
		sink, err := NewEventSink("session-1", 0, store, eventSinkFunc(func(context.Context, events.Event) error {
			broadcast = true
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		err = sink.Emit(context.Background(), testPublicEvent(t, 1, events.KindRunStarted, events.RunStarted{}))
		if !errors.Is(err, wantStore) || broadcast {
			t.Fatalf("Emit()=%v broadcast=%t", err, broadcast)
		}
	})
	t.Run("renderer failure remains committed", func(t *testing.T) {
		store := &fakeEventStore{}
		sink, err := NewEventSink("session-1", 0, store, eventSinkFunc(func(context.Context, events.Event) error {
			return wantRenderer
		}))
		if err != nil {
			t.Fatal(err)
		}
		err = sink.Emit(context.Background(), testPublicEvent(t, 1, events.KindRunStarted, events.RunStarted{}))
		if !errors.Is(err, wantRenderer) || len(store.records) != 1 {
			t.Fatalf("Emit()=%v records=%d", err, len(store.records))
		}
	})
}

func TestEventSinkUsesConfiguredAfterSequence(t *testing.T) {
	store := &fakeEventStore{}
	sink, err := NewEventSink("session-1", 7, store, &events.MemorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Emit(context.Background(), testPublicEvent(t, 9, events.KindWarning, events.Warning{Message: "x"})); err != nil {
		t.Fatal(err)
	}
	if store.expected != 7 || store.records[0].SessionSeq != 8 {
		t.Fatalf("expected=%d record=%+v", store.expected, store.records[0])
	}
}

func TestCommandEventSinkPersistsCommandIdentity(t *testing.T) {
	store := &fakeEventStore{}
	sink, err := NewCommandEventSink("session-1", "command-1", 0, store, &events.MemorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Emit(context.Background(), testPublicEvent(t, 1, events.KindRunStarted, events.RunStarted{})); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 1 || store.records[0].CommandID != "command-1" {
		t.Fatalf("records=%+v", store.records)
	}
}

func testPublicEvent(t *testing.T, seq uint64, kind events.Kind, payload any) events.Event {
	t.Helper()
	event, err := events.New("run-1", seq, time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC), kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

type fakeEventStore struct {
	expected uint64
	records  []Record
	err      error
	onAppend func()
}

func (store *fakeEventStore) Append(_ context.Context, _ string, expected uint64, records []Record) error {
	if store.onAppend != nil {
		store.onAppend()
	}
	if store.err != nil {
		return store.err
	}
	store.expected = expected
	for _, record := range records {
		store.records = append(store.records, cloneRecord(record))
	}
	return nil
}

func (*fakeEventStore) Load(context.Context, string, uint64, int) ([]Record, error) { return nil, nil }

type eventSinkFunc func(context.Context, events.Event) error

func (function eventSinkFunc) Emit(ctx context.Context, event events.Event) error {
	return function(ctx, event)
}
