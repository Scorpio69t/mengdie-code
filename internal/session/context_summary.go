// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	agentcontext "github.com/Scorpio69t/mengdie-code/internal/context"
)

const contextSummarySourceStart uint64 = 2

var ErrContextSummaryNotFound = errors.New("session context summary not found")

// ContextSummary is a private, derived navigation aid. Source messages remain
// authoritative in context_messages and are always validated independently.
type ContextSummary struct {
	ID                       string
	SessionID                string
	SourceStart              uint64
	SourceEnd                uint64
	RunID                    string
	CommandID                string
	Summary                  string
	SHA256                   string
	GeneratorModel           string
	GeneratorVersion         string
	EstimatedBefore          int
	EstimatedAfterUpperBound int
	CreatedAt                time.Time
}

func (r *ContextRecorder) RecordCompaction(ctx context.Context, record agentcontext.CompactionRecord) (receipt agentcontext.CompactionReceipt, resultErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	summary := strings.TrimSpace(record.Summary)
	switch {
	case summary == "":
		return receipt, errors.New("context summary is required")
	case len([]byte(summary)) > 64<<10:
		return receipt, errors.New("context summary exceeds 64 KiB")
	case strings.TrimSpace(record.GeneratorModel) == "" || len(record.GeneratorModel) > 256:
		return receipt, errors.New("context summary generator model is invalid")
	case strings.TrimSpace(record.GeneratorVersion) == "" || len(record.GeneratorVersion) > 128:
		return receipt, errors.New("context summary generator version is invalid")
	case record.RetainedTailMessages < 1 || uint64(record.RetainedTailMessages) >= r.last:
		return receipt, errors.New("context summary retained tail is invalid")
	case record.EstimatedBefore <= 0 || record.EstimatedAfterUpperBound <= 0:
		return receipt, errors.New("context summary token estimates are required")
	}
	if err := agentcontext.ValidateSummary(summary); err != nil {
		return receipt, err
	}
	sourceEnd := r.last - uint64(record.RetainedTailMessages)
	if sourceEnd < contextSummarySourceStart {
		return receipt, errors.New("context summary source range is empty")
	}

	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, classifySQLiteError("begin context summary append", err)
	}
	defer rollbackTransaction(tx, &resultErr, "rollback context summary append")
	var actual uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0) FROM context_messages WHERE session_id=?`, r.sessionID).Scan(&actual); err != nil {
		return receipt, classifySQLiteError("load context position for summary", err)
	}
	if actual != r.last {
		return receipt, fmt.Errorf("%w: expected %d, actual %d", ErrContextConflict, r.last, actual)
	}
	var previousEnd uint64
	err = tx.QueryRowContext(ctx, `SELECT source_end FROM context_summaries WHERE session_id=? ORDER BY source_end DESC LIMIT 1`, r.sessionID).Scan(&previousEnd)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return receipt, classifySQLiteError("load previous context summary", err)
	}
	if err == nil && sourceEnd <= previousEnd {
		return receipt, fmt.Errorf("%w: summary source end %d does not advance %d", ErrContextConflict, sourceEnd, previousEnd)
	}
	digest := sha256.Sum256([]byte(summary))
	checksum := fmt.Sprintf("sha256:%x", digest[:])
	idDigest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%s", r.sessionID, contextSummarySourceStart, sourceEnd, checksum)))
	id := fmt.Sprintf("sum_%x", idDigest[:])
	createdAt := formatTime(r.store.now().UTC())
	if _, err := tx.ExecContext(ctx, `
INSERT INTO context_summaries(
    id, session_id, source_start, source_end, run_id, command_id,
    summary_text, summary_sha256, generator_model, generator_version,
    estimated_before, estimated_after_upper_bound, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, r.sessionID, contextSummarySourceStart, sourceEnd, r.runID, r.commandID,
		[]byte(summary), checksum, record.GeneratorModel, record.GeneratorVersion,
		record.EstimatedBefore, record.EstimatedAfterUpperBound, createdAt,
	); err != nil {
		return receipt, classifySQLiteError("insert context summary", err)
	}
	if err := tx.Commit(); err != nil {
		return receipt, classifySQLiteError("commit context summary", err)
	}
	return agentcontext.CompactionReceipt{SourceStart: contextSummarySourceStart, SourceEnd: sourceEnd}, nil
}

func (s *SQLiteStore) LoadLatestContextSummary(ctx context.Context, sessionID string) (ContextSummary, error) {
	if strings.TrimSpace(sessionID) == "" {
		return ContextSummary{}, errors.New("context summary session id is required")
	}
	var item ContextSummary
	var summary []byte
	var createdAt string
	err := s.db.QueryRowContext(ctx, `
SELECT id, session_id, source_start, source_end, run_id, command_id,
       summary_text, summary_sha256, generator_model, generator_version,
       estimated_before, estimated_after_upper_bound, created_at
FROM context_summaries WHERE session_id=? ORDER BY source_end DESC LIMIT 1`, sessionID).Scan(
		&item.ID, &item.SessionID, &item.SourceStart, &item.SourceEnd, &item.RunID, &item.CommandID,
		&summary, &item.SHA256, &item.GeneratorModel, &item.GeneratorVersion,
		&item.EstimatedBefore, &item.EstimatedAfterUpperBound, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ContextSummary{}, ErrContextSummaryNotFound
	}
	if err != nil {
		return ContextSummary{}, classifySQLiteError("load context summary", err)
	}
	item.Summary = string(summary)
	digest := sha256.Sum256(summary)
	checksum := fmt.Sprintf("sha256:%x", digest[:])
	if item.SessionID != sessionID || item.SourceStart != contextSummarySourceStart || item.SourceEnd < item.SourceStart ||
		strings.TrimSpace(item.Summary) == "" || len(summary) > 64<<10 || item.SHA256 != checksum ||
		strings.TrimSpace(item.GeneratorModel) == "" || strings.TrimSpace(item.GeneratorVersion) == "" ||
		item.EstimatedBefore <= 0 || item.EstimatedAfterUpperBound <= 0 {
		return ContextSummary{}, fmt.Errorf("%w: invalid rolling summary", ErrContextCorrupt)
	}
	if err := agentcontext.ValidateSummary(item.Summary); err != nil {
		return ContextSummary{}, fmt.Errorf("%w: invalid rolling summary protocol: %v", ErrContextCorrupt, err)
	}
	var sourceCount uint64
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM context_messages WHERE session_id=? AND ordinal BETWEEN ? AND ?`,
		sessionID, item.SourceStart, item.SourceEnd,
	).Scan(&sourceCount); err != nil {
		return ContextSummary{}, classifySQLiteError("verify context summary source", err)
	}
	if sourceCount != item.SourceEnd-item.SourceStart+1 {
		return ContextSummary{}, fmt.Errorf("%w: rolling summary source range is incomplete", ErrContextCorrupt)
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return ContextSummary{}, fmt.Errorf("%w: invalid rolling summary time", ErrContextCorrupt)
	}
	return item, nil
}
