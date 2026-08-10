// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

const (
	MaxContextMessageBytes = 1 << 20
	maximumContextMessages = 4096
)

var (
	ErrContextConflict = errors.New("session context sequence conflict")
	ErrContextCorrupt  = errors.New("session context message is corrupt")
)

type ContextCompleteness string

const (
	ContextFull      ContextCompleteness = "full"
	ContextSanitized ContextCompleteness = "sanitized"
)

type ContextMessage struct {
	ID           string              `json:"id"`
	SessionID    string              `json:"session_id"`
	Ordinal      uint64              `json:"ordinal"`
	RunID        string              `json:"run_id"`
	CommandID    string              `json:"command_id"`
	Message      provider.Message    `json:"-"`
	Completeness ContextCompleteness `json:"completeness"`
	CreatedAt    time.Time           `json:"created_at"`
}

// ContextRecorder serializes private model-context facts for one Run. It is
// intentionally separate from public events and never broadcasts payloads.
type ContextRecorder struct {
	mu        sync.Mutex
	store     *SQLiteStore
	sessionID string
	runID     string
	commandID string
	last      uint64
}

func (s *SQLiteStore) NewContextRecorder(ctx context.Context, sessionID, runID, commandID string) (*ContextRecorder, error) {
	return s.newContextRecorder(ctx, sessionID, runID, commandID, nil)
}

// NewContextRecorderAt pins a resumed Run to the exact context position that
// passed analysis. A late writer from the interrupted Run therefore causes a
// conflict before the new user message or any Provider call can proceed.
func (s *SQLiteStore) NewContextRecorderAt(ctx context.Context, sessionID, runID, commandID string, expected uint64) (*ContextRecorder, error) {
	return s.newContextRecorder(ctx, sessionID, runID, commandID, &expected)
}

func (s *SQLiteStore) newContextRecorder(ctx context.Context, sessionID, runID, commandID string, expected *uint64) (*ContextRecorder, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(runID) == "" || strings.TrimSpace(commandID) == "" {
		return nil, errors.New("context recorder identities are required")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM runs WHERE id=? AND session_id=? AND command_id=?`, runID, sessionID, commandID).Scan(&count); err != nil {
		return nil, classifySQLiteError("verify context recorder identity", err)
	}
	if count != 1 {
		return nil, ErrRunConflict
	}
	last, err := s.contextOrdinal(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if expected != nil && last != *expected {
		return nil, fmt.Errorf("%w: expected %d, actual %d", ErrContextConflict, *expected, last)
	}
	return &ContextRecorder{store: s, sessionID: sessionID, runID: runID, commandID: commandID, last: last}, nil
}

// RecordMessage implements the Agent context recorder boundary. complete=false
// means output-bearing fields were replaced with a recovery-safe summary.
func (r *ContextRecorder) RecordMessage(ctx context.Context, message provider.Message, complete bool) (resultErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateContextProviderMessage(message); err != nil {
		return err
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode context message: %w", err)
	}
	if len(encoded) > MaxContextMessageBytes {
		return fmt.Errorf("context message exceeds %d bytes", MaxContextMessageBytes)
	}
	completeness := ContextSanitized
	if complete {
		completeness = ContextFull
	}
	digest := sha256.Sum256(encoded)
	checksum := fmt.Sprintf("sha256:%x", digest[:])
	ordinal := r.last + 1
	idDigest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", r.sessionID, ordinal, checksum)))
	id := fmt.Sprintf("ctx_%x", idDigest[:])
	createdAt := formatTime(r.store.now().UTC())

	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteError("begin context append", err)
	}
	defer rollbackTransaction(tx, &resultErr, "rollback context append")
	var actual uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0) FROM context_messages WHERE session_id=?`, r.sessionID).Scan(&actual); err != nil {
		return classifySQLiteError("load context position", err)
	}
	if actual != r.last {
		return fmt.Errorf("%w: expected %d, actual %d", ErrContextConflict, r.last, actual)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO context_messages(
    id, session_id, ordinal, run_id, command_id, role, completeness,
    message_json, message_sha256, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, r.sessionID, ordinal, r.runID, r.commandID, string(message.Role), string(completeness),
		encoded, checksum, createdAt,
	); err != nil {
		return classifySQLiteError("insert context message", err)
	}
	if err := tx.Commit(); err != nil {
		return classifySQLiteError("commit context message", err)
	}
	r.last = ordinal
	return nil
}

func (s *SQLiteStore) LoadContext(ctx context.Context, sessionID string) (result []ContextMessage, resultErr error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("context session id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, session_id, ordinal, run_id, command_id, role, completeness, message_json, message_sha256, created_at
FROM context_messages WHERE session_id=? ORDER BY ordinal LIMIT ?`, sessionID, maximumContextMessages+1)
	if err != nil {
		return nil, classifySQLiteError("load context messages", err)
	}
	defer closeRows(rows, &resultErr, "close context message rows")
	result = make([]ContextMessage, 0)
	for rows.Next() {
		var item ContextMessage
		var encoded []byte
		var persistedRole, checksum, createdAt string
		if err := rows.Scan(
			&item.ID, &item.SessionID, &item.Ordinal, &item.RunID, &item.CommandID,
			&persistedRole, &item.Completeness, &encoded, &checksum, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan context message: %w", err)
		}
		if len(result) == maximumContextMessages {
			return nil, fmt.Errorf("context exceeds %d messages", maximumContextMessages)
		}
		if item.Ordinal != uint64(len(result)+1) {
			return nil, fmt.Errorf("%w: ordinal gap at %d", ErrContextCorrupt, item.Ordinal)
		}
		digest := sha256.Sum256(encoded)
		if checksum != fmt.Sprintf("sha256:%x", digest[:]) {
			return nil, fmt.Errorf("%w: checksum mismatch at %d", ErrContextCorrupt, item.Ordinal)
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&item.Message); err != nil {
			return nil, fmt.Errorf("%w: decode message %d: %v", ErrContextCorrupt, item.Ordinal, err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: trailing message data at %d", ErrContextCorrupt, item.Ordinal)
		}
		if err := validateContextProviderMessage(item.Message); err != nil {
			return nil, fmt.Errorf("%w: message %d: %v", ErrContextCorrupt, item.Ordinal, err)
		}
		if persistedRole != string(item.Message.Role) {
			return nil, fmt.Errorf("%w: role mismatch at %d", ErrContextCorrupt, item.Ordinal)
		}
		if item.Completeness != ContextFull && item.Completeness != ContextSanitized {
			return nil, fmt.Errorf("%w: completeness at %d", ErrContextCorrupt, item.Ordinal)
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("%w: time at %d: %v", ErrContextCorrupt, item.Ordinal, err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, classifySQLiteError("iterate context messages", err)
	}
	return result, nil
}

func (s *SQLiteStore) contextOrdinal(ctx context.Context, sessionID string) (uint64, error) {
	var value uint64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0) FROM context_messages WHERE session_id=?`, sessionID).Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, classifySQLiteError("load context ordinal", err)
	}
	return value, nil
}

func validateContextProviderMessage(message provider.Message) error {
	request := provider.ChatRequest{Model: "context-validation", Messages: []provider.Message{message}}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate context message: %w", err)
	}
	return nil
}
