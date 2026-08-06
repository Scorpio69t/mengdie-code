// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRecordValidate(t *testing.T) {
	valid := Record{
		ID: "evt-1", SessionID: "session-1", SessionSeq: 1, RunID: "run-1", RunSeq: 1,
		Kind: "run.started", SchemaVersion: 1, Visibility: VisibilityPublic,
		Payload: json.RawMessage(`{"model":"test"}`), Time: time.Now(),
	}
	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "id", mutate: func(record *Record) { record.ID = "" }},
		{name: "session", mutate: func(record *Record) { record.SessionID = "" }},
		{name: "session limit", mutate: func(record *Record) { record.SessionID = strings.Repeat("s", 257) }},
		{name: "session seq", mutate: func(record *Record) { record.SessionSeq = 0 }},
		{name: "run", mutate: func(record *Record) { record.RunID = "" }},
		{name: "run limit", mutate: func(record *Record) { record.RunID = strings.Repeat("r", 257) }},
		{name: "run seq", mutate: func(record *Record) { record.RunSeq = 0 }},
		{name: "kind", mutate: func(record *Record) { record.Kind = "" }},
		{name: "command limit", mutate: func(record *Record) { record.CommandID = strings.Repeat("c", 257) }},
		{name: "version", mutate: func(record *Record) { record.SchemaVersion = 0 }},
		{name: "visibility", mutate: func(record *Record) { record.Visibility = "secret" }},
		{name: "payload", mutate: func(record *Record) { record.Payload = json.RawMessage(`{`) }},
		{name: "payload limit", mutate: func(record *Record) {
			record.Payload = json.RawMessage(`"` + strings.Repeat("x", MaxRecordPayloadBytes) + `"`)
		}},
		{name: "time", mutate: func(record *Record) { record.Time = time.Time{} }},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid record: %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := cloneRecord(valid)
			test.mutate(&record)
			if err := record.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
}
