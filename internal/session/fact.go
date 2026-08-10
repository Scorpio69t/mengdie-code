// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
)

// PublicFact is the sanitized application envelope shared with replaceable
// interfaces such as the TUI. Private command/context data and persistence
// details deliberately have no representation here.
type PublicFact struct {
	SessionID     string          `json:"session_id"`
	SessionSeq    uint64          `json:"session_seq"`
	RunID         string          `json:"run_id"`
	RunSeq        uint64          `json:"run_seq"`
	Kind          events.Kind     `json:"kind"`
	SchemaVersion uint16          `json:"schema_version"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Time          time.Time       `json:"time"`
}

// PublicFactPage is one ordered EventStore replay page. ThroughSeq advances
// across filtered private/metadata records so callers do not repeatedly scan
// hidden facts. More is conservative: true means another read is required to
// prove the caller has caught up.
type PublicFactPage struct {
	Facts      []PublicFact `json:"facts"`
	ThroughSeq uint64       `json:"through_seq"`
	More       bool         `json:"more"`
}

func publicFactFromRecord(record Record) (PublicFact, bool) {
	if record.Visibility != VisibilityPublic {
		return PublicFact{}, false
	}
	return PublicFact{
		SessionID: record.SessionID, SessionSeq: record.SessionSeq,
		RunID: record.RunID, RunSeq: record.RunSeq, Kind: events.Kind(record.Kind),
		SchemaVersion: record.SchemaVersion,
		Payload:       append(json.RawMessage(nil), record.Payload...),
		Time:          record.Time,
	}, true
}

func (fact PublicFact) validate() error {
	switch {
	case strings.TrimSpace(fact.SessionID) == "":
		return errors.New("public fact session id is required")
	case fact.SessionSeq == 0:
		return errors.New("public fact session sequence is required")
	case strings.TrimSpace(fact.RunID) == "":
		return errors.New("public fact run id is required")
	case fact.RunSeq == 0:
		return errors.New("public fact run sequence is required")
	case strings.TrimSpace(string(fact.Kind)) == "":
		return errors.New("public fact kind is required")
	case fact.SchemaVersion == 0:
		return errors.New("public fact schema version is required")
	case len(fact.Payload) == 0 || !json.Valid(fact.Payload):
		return errors.New("public fact payload must be valid JSON")
	case fact.Time.IsZero():
		return errors.New("public fact time is required")
	default:
		return nil
	}
}

func clonePublicFact(fact PublicFact) PublicFact {
	fact.Payload = append(fact.Payload[:0:0], fact.Payload...)
	return fact
}

func (fact PublicFact) record() Record {
	return Record{
		ID: "public-fact", SessionID: fact.SessionID, SessionSeq: fact.SessionSeq,
		RunID: fact.RunID, RunSeq: fact.RunSeq, Kind: string(fact.Kind),
		SchemaVersion: fact.SchemaVersion, Visibility: VisibilityPublic,
		Payload: append(json.RawMessage(nil), fact.Payload...), Time: fact.Time,
	}
}

// ReducePublicFacts applies sanitized facts through the same deterministic
// reducer used for a full SessionView replay.
func ReducePublicFacts(base SessionView, facts []PublicFact) (SessionView, error) {
	records := make([]Record, 0, len(facts))
	for index, fact := range facts {
		if err := fact.validate(); err != nil {
			return SessionView{}, fmt.Errorf("public fact %d: %w", index, err)
		}
		records = append(records, fact.record())
	}
	return Reduce(base, records)
}
