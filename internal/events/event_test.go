// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestNewValidatesEnvelope(t *testing.T) {
	at := time.Date(2026, 7, 30, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	event, err := New("run-1", 1, at, KindRunStarted, RunStarted{Model: "deepseek:chat"})
	if err != nil {
		t.Fatal(err)
	}
	if event.Version != SchemaVersion || event.Time.Location() != time.UTC {
		t.Fatalf("event = %+v", event)
	}
	payload, err := DecodePayload[RunStarted](event)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Model != "deepseek:chat" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestValidateRejectsInvalidEnvelope(t *testing.T) {
	valid := Event{RunID: "run-1", Seq: 1, Version: SchemaVersion, Time: time.Now(), Kind: KindWarning, Payload: []byte(`{}`)}
	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{"run id", func(event *Event) { event.RunID = "" }},
		{"sequence", func(event *Event) { event.Seq = 0 }},
		{"version", func(event *Event) { event.Version = 2 }},
		{"time", func(event *Event) { event.Time = time.Time{} }},
		{"kind", func(event *Event) { event.Kind = "" }},
		{"payload", func(event *Event) { event.Payload = []byte(`{`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := valid
			test.mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatalf("Validate() accepted %+v", event)
			}
		})
	}
}

func TestEmitterSerializesConcurrentEvents(t *testing.T) {
	const count = 64
	sink := &MemorySink{}
	at := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	emitter, err := NewEmitter("run-concurrent", sink, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range count {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, emitErr := emitter.Emit(context.Background(), KindWarning, Warning{Message: "test"}); emitErr != nil {
				t.Errorf("Emit() error = %v", emitErr)
			}
		}()
	}
	group.Wait()
	events := sink.Events()
	if len(events) != count {
		t.Fatalf("len(Events()) = %d, want %d", len(events), count)
	}
	for i, event := range events {
		if event.Seq != uint64(i+1) {
			t.Fatalf("events[%d].Seq = %d", i, event.Seq)
		}
	}
}

func TestEmitterPropagatesContextAndSinkErrors(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	emitter, err := NewEmitter("run-1", failingSink{err: errors.New("write failed")}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emitter.Emit(cancelled, KindWarning, Warning{Message: "test"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Emit() error = %v", err)
	}
	if _, err := emitter.Emit(context.Background(), KindWarning, Warning{Message: "test"}); err == nil || err.Error() != "write failed" {
		t.Fatalf("Emit() error = %v", err)
	}
}

type failingSink struct{ err error }

func (s failingSink) Emit(context.Context, Event) error { return s.err }
