// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const MaxCommandPayloadBytes = 1 << 20

const CommandKindExec = "exec"

var (
	ErrCommandConflict = errors.New("command id already belongs to different input")
	ErrCommandNotFound = errors.New("command not found")
)

type CommandStatus string

const (
	CommandAccepted    CommandStatus = "accepted"
	CommandRunning     CommandStatus = "running"
	CommandApplied     CommandStatus = "applied"
	CommandRejected    CommandStatus = "rejected"
	CommandFailed      CommandStatus = "failed"
	CommandInterrupted CommandStatus = "interrupted"
)

// Command is the private idempotency ledger entry. Payload is intentionally
// never included in SessionView or CLI projections.
type Command struct {
	ID            string          `json:"id"`
	SessionID     string          `json:"session_id"`
	Kind          string          `json:"kind"`
	Payload       json.RawMessage `json:"-"`
	PayloadSHA256 string          `json:"-"`
	Status        CommandStatus   `json:"status"`
	ResultSeq     uint64          `json:"result_seq,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// CommandRunMetadata atomically registers the independent session, command
// and run identities before any external side effect is allowed.
type CommandRunMetadata struct {
	SessionID       string
	CommandID       string
	CommandKind     string
	CommandPayload  json.RawMessage
	RunID           string
	ProjectRoot     string
	ProjectIdentity string
	Provider        string
	Model           string
	StartedAt       time.Time
}

type BeginCommandResult struct {
	Command  Command
	RunID    string
	Existing bool
}

// TaskCommandPayload returns the canonical private payload used by the exec
// command ledger. encoding/json sorts map keys, while this fixed struct also
// keeps the representation stable across runs.
func TaskCommandPayload(task string) (json.RawMessage, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return nil, errors.New("command task is required")
	}
	payload, err := json.Marshal(struct {
		Task string `json:"task"`
	}{Task: task})
	if err != nil {
		return nil, fmt.Errorf("encode command task: %w", err)
	}
	return payload, nil
}

func commandPayloadDigest(payload json.RawMessage) (string, error) {
	if len(payload) == 0 || !json.Valid(payload) {
		return "", errors.New("command payload must be valid JSON")
	}
	if len(payload) > MaxCommandPayloadBytes {
		return "", fmt.Errorf("command payload exceeds %d bytes", MaxCommandPayloadBytes)
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return "", fmt.Errorf("decode command payload: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize command payload: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

func validCommandStatus(status CommandStatus) bool {
	switch status {
	case CommandAccepted, CommandRunning, CommandApplied, CommandRejected, CommandFailed, CommandInterrupted:
		return true
	default:
		return false
	}
}
