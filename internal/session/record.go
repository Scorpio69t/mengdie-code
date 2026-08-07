// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package session owns durable, process-independent session facts.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// MaxRecordPayloadBytes bounds one durable fact. Larger material belongs in
	// the Artifact Store introduced by a later M2 slice.
	MaxRecordPayloadBytes = 1 << 20
	maxBatchRecords       = 256
)

// Visibility controls which durable facts may be projected to public output.
type Visibility string

const (
	VisibilityPrivate  Visibility = "private"
	VisibilityPublic   Visibility = "public"
	VisibilityMetadata Visibility = "metadata"
)

// Record is the durable M2 envelope. SessionSeq is independent from the M1
// public event Seq, which remains run-scoped for compatibility.
type Record struct {
	ID            string
	SessionID     string
	SessionSeq    uint64
	RunID         string
	RunSeq        uint64
	CommandID     string
	Kind          string
	SchemaVersion uint16
	Visibility    Visibility
	Payload       json.RawMessage
	Time          time.Time
}

// Validate rejects ambiguous or unbounded durable records while accepting
// unknown future kinds.
func (r Record) Validate() error {
	switch {
	case strings.TrimSpace(r.ID) == "":
		return errors.New("session record id is required")
	case len(r.ID) > 256:
		return errors.New("session record id exceeds 256 bytes")
	case strings.TrimSpace(r.SessionID) == "":
		return errors.New("session record session_id is required")
	case len(r.SessionID) > 256:
		return errors.New("session record session_id exceeds 256 bytes")
	case r.SessionSeq == 0:
		return errors.New("session record session_seq must be greater than zero")
	case strings.TrimSpace(r.RunID) == "":
		return errors.New("session record run_id is required")
	case len(r.RunID) > 256:
		return errors.New("session record run_id exceeds 256 bytes")
	case r.RunSeq == 0:
		return errors.New("session record run_seq must be greater than zero")
	case strings.TrimSpace(r.Kind) == "":
		return errors.New("session record kind is required")
	case len(r.Kind) > 128:
		return errors.New("session record kind exceeds 128 bytes")
	case len(r.CommandID) > 256:
		return errors.New("session record command_id exceeds 256 bytes")
	case r.SchemaVersion == 0:
		return errors.New("session record schema version is required")
	case r.Visibility != VisibilityPrivate && r.Visibility != VisibilityPublic && r.Visibility != VisibilityMetadata:
		return fmt.Errorf("unsupported session record visibility %q", r.Visibility)
	case len(r.Payload) == 0 || !json.Valid(r.Payload):
		return errors.New("session record payload must be valid JSON")
	case len(r.Payload) > MaxRecordPayloadBytes:
		return fmt.Errorf("session record payload exceeds %d bytes", MaxRecordPayloadBytes)
	case r.Time.IsZero():
		return errors.New("session record time is required")
	default:
		return nil
	}
}

func cloneRecord(record Record) Record {
	record.Payload = append(record.Payload[:0:0], record.Payload...)
	return record
}

// EventStore is the durable fact boundary used by the current event adapter.
// Future resume consumers can define narrower read interfaces at their call
// sites instead of depending on the SQLite implementation.
type EventStore interface {
	Append(ctx context.Context, sessionID string, expectedSeq uint64, records []Record) error
	Load(ctx context.Context, sessionID string, afterSeq uint64, limit int) ([]Record, error)
}
