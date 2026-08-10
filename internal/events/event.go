// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package events defines the versioned, UI-independent event protocol used by
// the MengDie application harness.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SchemaVersion is the only event envelope version emitted by M1.
const SchemaVersion uint16 = 1

// Kind identifies a stable event shape.
type Kind string

const (
	KindRunStarted       Kind = "run.started"
	KindMessageDelta     Kind = "message.delta"
	KindMessageCompleted Kind = "message.completed"
	KindTodoUpdated      Kind = "todo.updated"
	KindToolProposed     Kind = "tool.proposed"
	KindApprovalNeeded   Kind = "approval.needed"
	KindApprovalResolved Kind = "approval.resolved"
	KindToolStarted      Kind = "tool.started"
	KindToolCompleted    Kind = "tool.completed"
	KindRecoveryResolved Kind = "recovery.resolved"
	KindUsageUpdated     Kind = "usage.updated"
	KindWarning          Kind = "warning"
	KindRunCompleted     Kind = "run.completed"
	KindRunFailed        Kind = "run.failed"
	KindRunCancelled     Kind = "run.cancelled"
)

// Event is the in-memory M1 envelope. Session identity and persistence belong
// to M2; RunID, Seq and Version are intentionally present now so renderers do
// not need a protocol rewrite later.
type Event struct {
	RunID   string          `json:"run_id"`
	Seq     uint64          `json:"seq"`
	Version uint16          `json:"version"`
	Time    time.Time       `json:"time"`
	Kind    Kind            `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// New constructs and validates one event with a typed payload.
func New(runID string, seq uint64, at time.Time, kind Kind, payload any) (Event, error) {
	var raw json.RawMessage
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return Event{}, fmt.Errorf("encode %s payload: %w", kind, err)
		}
		raw = encoded
	}
	event := Event{
		RunID:   runID,
		Seq:     seq,
		Version: SchemaVersion,
		Time:    at.UTC(),
		Kind:    kind,
		Payload: raw,
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

// Validate checks envelope invariants without rejecting unknown future kinds.
func (e Event) Validate() error {
	switch {
	case strings.TrimSpace(e.RunID) == "":
		return errors.New("event run_id is required")
	case e.Seq == 0:
		return errors.New("event seq must be greater than zero")
	case e.Version != SchemaVersion:
		return fmt.Errorf("unsupported event version %d", e.Version)
	case e.Time.IsZero():
		return errors.New("event time is required")
	case strings.TrimSpace(string(e.Kind)) == "":
		return errors.New("event kind is required")
	case len(e.Payload) > 0 && !json.Valid(e.Payload):
		return errors.New("event payload must be valid JSON")
	default:
		return nil
	}
}

// DecodePayload decodes a known payload and preserves the event kind in errors.
func DecodePayload[T any](event Event) (T, error) {
	var payload T
	if len(event.Payload) == 0 {
		return payload, fmt.Errorf("%s payload is required", event.Kind)
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return payload, fmt.Errorf("decode %s payload: %w", event.Kind, err)
	}
	return payload, nil
}

// RunStarted describes non-sensitive, user-visible facts available at run
// start. The original task/prompt is intentionally excluded from the event
// stream so JSON Lines consumers do not accidentally persist it.
type RunStarted struct {
	Model    string `json:"model,omitempty"`
	CWD      string `json:"cwd,omitempty"`
	Security string `json:"security,omitempty"`
}

type MessageDelta struct {
	Text string `json:"text"`
}

type MessageCompleted struct {
	Text string `json:"text,omitempty"`
}

type Todo struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

type TodoUpdated struct {
	Todos []Todo `json:"todos"`
}

type ToolProposed struct {
	CallID  string   `json:"call_id"`
	Tool    string   `json:"tool"`
	Summary string   `json:"summary,omitempty"`
	Effects []string `json:"effects,omitempty"`
}

type ApprovalNeeded struct {
	CallID string `json:"call_id"`
	Prompt string `json:"prompt"`
	Risk   string `json:"risk,omitempty"`
}

type ApprovalResolved struct {
	CallID   string `json:"call_id"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

type ToolStarted struct {
	CallID string `json:"call_id"`
	Tool   string `json:"tool"`
}

type ToolCompleted struct {
	CallID     string `json:"call_id"`
	Tool       string `json:"tool"`
	Success    bool   `json:"success"`
	Summary    string `json:"summary,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// RecoveryResolved binds a new-run tool result to an interrupted source tool
// call. It intentionally contains only stable identities and an outcome, not
// tool arguments, previews, source text, command output, or credentials.
type RecoveryResolved struct {
	SourceRunID string `json:"source_run_id"`
	CallID      string `json:"call_id"`
	Action      string `json:"action"`
	Outcome     string `json:"outcome"`
}

type UsageUpdated struct {
	InputTokens     int64 `json:"input_tokens,omitempty"`
	OutputTokens    int64 `json:"output_tokens,omitempty"`
	CacheReadTokens int64 `json:"cache_read_tokens,omitempty"`
}

type Warning struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

type RunCompleted struct {
	Summary     string `json:"summary,omitempty"`
	DeniedTools int    `json:"denied_tools,omitempty"`
}

type RunFailed struct {
	Category  string `json:"category"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type RunCancelled struct {
	Reason string `json:"reason"`
}
