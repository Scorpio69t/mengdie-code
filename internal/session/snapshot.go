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
	"time"
)

var (
	ErrSnapshotNotFound     = errors.New("session snapshot not found")
	ErrSnapshotCorrupt      = errors.New("session snapshot is corrupt")
	ErrSnapshotIncompatible = errors.New("session snapshot schema is incompatible")
	ErrSnapshotConflict     = errors.New("session snapshot sequence conflict")
)

type Snapshot struct {
	SessionID     string
	ThroughSeq    uint64
	SchemaVersion uint16
	State         SessionView
	CreatedAt     time.Time
}

// SaveSnapshot replaces a cache only when its durable predecessor still has
// expectedThroughSeq. Facts in events remain authoritative.
func (s *SQLiteStore) SaveSnapshot(ctx context.Context, view SessionView, expectedThroughSeq uint64) (resultErr error) {
	if view.ID == "" {
		return errors.New("snapshot session id is required")
	}
	if view.LastSeq < expectedThroughSeq {
		return errors.New("snapshot cannot move backwards")
	}
	normalizeSessionView(&view)
	state, err := json.Marshal(view)
	if err != nil {
		return fmt.Errorf("encode session snapshot: %w", err)
	}
	digest := sha256.Sum256(state)
	checksum := fmt.Sprintf("sha256:%x", digest[:])
	createdAt := formatTime(s.now().UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteError("begin snapshot save", err)
	}
	defer rollbackTransaction(tx, &resultErr, "rollback snapshot save")
	var actual uint64
	err = tx.QueryRowContext(ctx, `SELECT through_seq FROM snapshots WHERE session_id=?`, view.ID).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		actual = 0
	} else if err != nil {
		return classifySQLiteError("load snapshot position", err)
	}
	if actual != expectedThroughSeq {
		return fmt.Errorf("%w: expected %d, actual %d", ErrSnapshotConflict, expectedThroughSeq, actual)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO snapshots(session_id, through_seq, schema_version, state_json, state_sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
    through_seq=excluded.through_seq,
    schema_version=excluded.schema_version,
    state_json=excluded.state_json,
    state_sha256=excluded.state_sha256,
    created_at=excluded.created_at`,
		view.ID, view.LastSeq, SnapshotSchemaVersion, state, checksum, createdAt,
	); err != nil {
		return classifySQLiteError("save snapshot", err)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE sessions SET snapshot_seq=? WHERE id=? AND last_seq>=?`, view.LastSeq, view.ID, view.LastSeq,
	)
	if err != nil {
		return classifySQLiteError("advance session snapshot", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect session snapshot advance: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("save snapshot: %w", ErrSessionNotFound)
	}
	if err := tx.Commit(); err != nil {
		return classifySQLiteError("commit snapshot", err)
	}
	return nil
}

func (s *SQLiteStore) LoadSnapshot(ctx context.Context, sessionID string) (Snapshot, error) {
	var throughSeq uint64
	var schemaVersion uint16
	var sessionLastSeq, indexedSnapshotSeq uint64
	var state []byte
	var checksum, createdAt string
	err := s.db.QueryRowContext(ctx, `
SELECT p.through_seq, p.schema_version, p.state_json, p.state_sha256, p.created_at,
       s.last_seq, s.snapshot_seq
FROM snapshots p JOIN sessions s ON s.id=p.session_id
WHERE p.session_id=?`, sessionID).Scan(
		&throughSeq, &schemaVersion, &state, &checksum, &createdAt, &sessionLastSeq, &indexedSnapshotSeq,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrSnapshotNotFound
	}
	if err != nil {
		return Snapshot{}, classifySQLiteError("load snapshot", err)
	}
	if schemaVersion != SnapshotSchemaVersion {
		return Snapshot{}, fmt.Errorf("%w: got %d, want %d", ErrSnapshotIncompatible, schemaVersion, SnapshotSchemaVersion)
	}
	if throughSeq > sessionLastSeq || indexedSnapshotSeq != throughSeq {
		return Snapshot{}, fmt.Errorf("%w: sequence index mismatch", ErrSnapshotCorrupt)
	}
	digest := sha256.Sum256(state)
	if checksum != fmt.Sprintf("sha256:%x", digest[:]) {
		return Snapshot{}, fmt.Errorf("%w: checksum mismatch", ErrSnapshotCorrupt)
	}
	var view SessionView
	decoder := json.NewDecoder(bytes.NewReader(state))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&view); err != nil {
		return Snapshot{}, fmt.Errorf("%w: decode state: %v", ErrSnapshotCorrupt, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Snapshot{}, fmt.Errorf("%w: trailing state data", ErrSnapshotCorrupt)
	}
	if view.ID != sessionID || view.LastSeq != throughSeq {
		return Snapshot{}, fmt.Errorf("%w: identity or sequence mismatch", ErrSnapshotCorrupt)
	}
	stamp, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: decode time: %v", ErrSnapshotCorrupt, err)
	}
	normalizeSessionView(&view)
	return Snapshot{SessionID: sessionID, ThroughSeq: throughSeq, SchemaVersion: schemaVersion, State: view, CreatedAt: stamp}, nil
}

func (s *SQLiteStore) DeleteSnapshot(ctx context.Context, sessionID string) (resultErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteError("begin snapshot delete", err)
	}
	defer rollbackTransaction(tx, &resultErr, "rollback snapshot delete")
	if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE session_id=?`, sessionID); err != nil {
		return classifySQLiteError("delete snapshot", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET snapshot_seq=0 WHERE id=?`, sessionID); err != nil {
		return classifySQLiteError("reset session snapshot", err)
	}
	if err := tx.Commit(); err != nil {
		return classifySQLiteError("commit snapshot delete", err)
	}
	return nil
}
