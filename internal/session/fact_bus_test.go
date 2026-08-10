// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
)

func TestPublicFactBusIsBoundedSignalsGapAndCloses(t *testing.T) {
	bus := NewPublicFactBus(1)
	subscription, err := bus.Subscribe("session-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	bus.PublishCommitted(publicWarningFact(t, 1, "first"))
	bus.PublishCommitted(publicWarningFact(t, 2, "second"))
	notification := <-subscription.Notifications()
	if notification.Fact.SessionSeq != 2 || !notification.Gap {
		t.Fatalf("notification=%+v", notification)
	}
	subscription.Close()
	subscription.Close()
	if _, open := <-subscription.Notifications(); open {
		t.Fatal("subscription channel remains open")
	}
	// Publishing after close must remain safe and non-blocking.
	bus.PublishCommitted(publicWarningFact(t, 3, "third"))
}

func TestPublicFactBusFiltersSessionAndAfterSequence(t *testing.T) {
	bus := NewPublicFactBus(2)
	subscription, err := bus.Subscribe("session-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	bus.PublishCommitted(publicWarningFact(t, 4, "duplicate"))
	other := publicWarningFact(t, 5, "other")
	other.SessionID = "session-2"
	bus.PublishCommitted(other)
	bus.PublishCommitted(publicWarningFact(t, 5, "next"))
	notification := <-subscription.Notifications()
	if notification.Fact.SessionSeq != 5 || notification.Gap {
		t.Fatalf("notification=%+v", notification)
	}
	select {
	case extra := <-subscription.Notifications():
		t.Fatalf("unexpected notification=%+v", extra)
	default:
	}
}

func TestReplayPublicFactsFiltersSecretsAndAdvancesAcrossHiddenRecords(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginTestRun(t, store)
	records := []Record{
		factTestRecord(t, 1, VisibilityPublic, events.KindWarning, events.Warning{Message: "visible-1"}),
		factTestRecord(t, 2, VisibilityPrivate, events.KindMessageCompleted, events.MessageCompleted{Text: "TOP-SECRET"}),
		factTestRecord(t, 3, VisibilityMetadata, events.KindWarning, events.Warning{Message: "metadata-secret"}),
		factTestRecord(t, 4, VisibilityPublic, events.KindWarning, events.Warning{Message: "visible-2"}),
	}
	if err := store.Append(context.Background(), "session-1", 0, records); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, WithPublicFactBus(NewPublicFactBus(1)))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.ReplayPublicFacts(context.Background(), "session-1", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Facts) != 1 || first.Facts[0].SessionSeq != 1 || first.ThroughSeq != 2 || !first.More {
		t.Fatalf("first=%+v", first)
	}
	second, err := service.ReplayPublicFacts(context.Background(), "session-1", first.ThroughSeq, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Facts) != 1 || second.Facts[0].SessionSeq != 4 || second.ThroughSeq != 4 || !second.More {
		t.Fatalf("second=%+v", second)
	}
	last, err := service.ReplayPublicFacts(context.Background(), "session-1", second.ThroughSeq, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Facts) != 0 || last.ThroughSeq != 4 || last.More {
		t.Fatalf("last=%+v", last)
	}
	encoded, err := json.Marshal([]PublicFactPage{first, second, last})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "TOP-SECRET") || strings.Contains(string(encoded), "metadata-secret") {
		t.Fatalf("private payload leaked: %s", encoded)
	}
	view, err := service.View(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.LastSeq != 4 || len(view.Messages) != 0 || len(view.Warnings) != 2 {
		t.Fatalf("view=%+v", view)
	}
}

func publicWarningFact(t *testing.T, sequence uint64, message string) PublicFact {
	t.Helper()
	event, err := events.New("run-1", sequence, storeTestTime.Add(time.Duration(sequence)*time.Second), events.KindWarning, events.Warning{Message: message})
	if err != nil {
		t.Fatal(err)
	}
	return PublicFact{
		SessionID: "session-1", SessionSeq: sequence, RunID: event.RunID,
		RunSeq: event.Seq, Kind: event.Kind, SchemaVersion: event.Version,
		Payload: event.Payload, Time: event.Time,
	}
}

func factTestRecord(t *testing.T, sequence uint64, visibility Visibility, kind events.Kind, payload any) Record {
	t.Helper()
	event, err := events.New("run-1", sequence, storeTestTime.Add(time.Duration(sequence)*time.Second), kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return Record{
		ID: fmt.Sprintf("fact-test-%d", sequence), SessionID: "session-1", SessionSeq: sequence,
		RunID: event.RunID, RunSeq: event.Seq, Kind: string(event.Kind), SchemaVersion: event.Version,
		Visibility: visibility, Payload: event.Payload, Time: event.Time,
	}
}
