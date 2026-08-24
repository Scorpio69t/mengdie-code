// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// TestEventReaderInterfaceSatisfiedByFake is a compile-time + behavioural
// check: any concrete type implementing the contract must work as an
// EventReader, and Rules.Extract must consume its rows without knowing the
// concrete type. The fake satisfies the interface by structural typing —
// that is the contract.
func TestEventReaderInterfaceSatisfiedByFake(t *testing.T) {
	reader := fakeEventReader{rows: []session.EventRow{
		{Kind: "tool.completed", Name: "edit_file", Success: true},
	}}
	var iface EventReader = reader
	rows, err := iface.Events(context.Background(), "session-1", 100)
	if err != nil {
		t.Fatalf("Events returned error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row from fake, got %d", len(rows))
	}
	want := session.EventRow{Kind: "tool.completed", Name: "edit_file", Success: true}
	if rows[0] != want {
		t.Fatalf("row mismatch: want %+v, got %+v", want, rows[0])
	}
}

// fakeEventReader is the test double used by Rules.Extract tests and the
// interface contract test. It returns a small, deterministic row set so
// downstream rule tests stay independent of any SQLite fixture.
type fakeEventReader struct {
	rows []session.EventRow
	err  error
}

func (f fakeEventReader) Events(_ context.Context, _ string, _ int) ([]session.EventRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.rows == nil {
		return nil, nil
	}
	out := make([]session.EventRow, len(f.rows))
	copy(out, f.rows)
	return out, nil
}

// TestRulesExtractReadsFromReader wires Rules through the EventReader
// contract: a fake reader injects one tool.completed/edit_file event and
// Rules.Extract must surface the repository claim. This is the Task 4
// acceptance test for the loadEvents replacement.
func TestRulesExtractReadsFromReader(t *testing.T) {
	reader := fakeEventReader{rows: []session.EventRow{
		{Kind: "tool.completed", Name: "edit_file", Success: true},
	}}
	rules := NewRules(reader)
	got, err := rules.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	found := false
	for _, mem := range got {
		if mem.Authority == memory.AuthorityRepository && contains(mem.Claim, "edit_file") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected repository claim for edit_file, got %+v", got)
	}
}

// TestRulesExtractNoReaderReturnsNil documents the defensive contract: a
// Rules built without an EventReader MUST return (nil, nil) so app.Runtime
// short-circuits cleanly while the wiring is being assembled.
func TestRulesExtractNoReaderReturnsNil(t *testing.T) {
	rules := &Rules{}
	got, err := rules.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil memories when reader is nil, got %+v", got)
	}
}

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
