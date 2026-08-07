// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	databaseFilename   = "state.db"
	defaultLoadLimit   = 200
	maximumLoadLimit   = 1000
	defaultBusyTimeout = 5 * time.Second
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrRunConflict     = errors.New("session run identity conflict")
	ErrDuplicateEvent  = errors.New("duplicate session event")
	ErrStoreBusy       = errors.New("session store is busy")
	ErrStoreFull       = errors.New("session store is full")
)

// SequenceConflictError reports optimistic concurrency failure without
// discarding the caller's expected and currently durable positions.
type SequenceConflictError struct {
	Expected uint64
	Actual   uint64
}

func (e *SequenceConflictError) Error() string {
	return fmt.Sprintf("session sequence conflict: expected %d, actual %d", e.Expected, e.Actual)
}

// OpenOptions configures one local SQLite fact store.
type OpenOptions struct {
	DataDir     string
	ProjectRoot string
	BusyTimeout time.Duration
	Now         func() time.Time
}

// RunMetadata describes the transitional one-run session created by P2-02.
type RunMetadata struct {
	SessionID       string
	RunID           string
	ProjectRoot     string
	ProjectIdentity string
	Provider        string
	Model           string
	StartedAt       time.Time
}

// SQLiteStore is the local M2 EventStore adapter. It deliberately exposes no
// database/sql types to callers.
type SQLiteStore struct {
	db   *sql.DB
	path string
	now  func() time.Time
}

// OpenSQLite opens, verifies and migrates the local event database.
func OpenSQLite(ctx context.Context, options OpenOptions) (*SQLiteStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(options.DataDir) == "" {
		return nil, errors.New("session data directory is required")
	}
	directory, err := filepath.Abs(options.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve session data directory: %w", err)
	}
	if err := validateDataDir(filepath.Clean(directory), options.ProjectRoot); err != nil {
		return nil, err
	}
	if err := prepareDataDir(directory); err != nil {
		return nil, err
	}
	busyTimeout := options.BusyTimeout
	if busyTimeout == 0 {
		busyTimeout = defaultBusyTimeout
	}
	if busyTimeout < 0 || busyTimeout > time.Minute {
		return nil, errors.New("session busy timeout must be between zero and one minute")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	databasePath := filepath.Join(directory, databaseFilename)
	db, err := sql.Open("sqlite", sqliteDSN(databasePath, busyTimeout))
	if err != nil {
		return nil, fmt.Errorf("open session database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	store := &SQLiteStore{db: db, path: databasePath, now: now}
	if err := db.PingContext(ctx); err != nil {
		return nil, closeDatabaseAfterError(db, classifySQLiteError("connect session database", err))
	}
	if err := verifyPragmas(ctx, db, busyTimeout); err != nil {
		return nil, closeDatabaseAfterError(db, err)
	}
	if err := applyMigrations(ctx, db, embeddedMigrations, now); err != nil {
		return nil, closeDatabaseAfterError(db, err)
	}
	if err := protectDataFile(databasePath); err != nil {
		return nil, closeDatabaseAfterError(db, err)
	}
	return store, nil
}

func sqliteDSN(filename string, busyTimeout time.Duration) string {
	slash := filepath.ToSlash(filename)
	if runtime.GOOS == "windows" && len(slash) >= 2 && slash[1] == ':' {
		slash = "/" + slash
	}
	uri := url.URL{Scheme: "file", Path: slash}
	query := url.Values{}
	query.Set("_busy_timeout", strconv.FormatInt(busyTimeout.Milliseconds(), 10))
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "wal")
	query.Set("_synchronous", "full")
	query.Set("_txlock", "immediate")
	uri.RawQuery = query.Encode()
	return uri.String()
}

func verifyPragmas(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	checks := []struct {
		query string
		want  string
	}{
		{query: `PRAGMA journal_mode`, want: "wal"},
		{query: `PRAGMA foreign_keys`, want: "1"},
		{query: `PRAGMA synchronous`, want: "2"},
		{query: `PRAGMA busy_timeout`, want: strconv.FormatInt(timeout.Milliseconds(), 10)},
	}
	for _, check := range checks {
		var value string
		if err := db.QueryRowContext(ctx, check.query).Scan(&value); err != nil {
			return classifySQLiteError("verify SQLite connection settings", err)
		}
		if !strings.EqualFold(strings.TrimSpace(value), check.want) {
			return fmt.Errorf("verify SQLite connection settings: %s=%q, want %q", check.query, value, check.want)
		}
	}
	return nil
}

// BeginRun creates the session and run metadata before the first durable fact.
func (s *SQLiteStore) BeginRun(ctx context.Context, metadata RunMetadata) (resultErr error) {
	if err := validateRunMetadata(metadata); err != nil {
		return err
	}
	if metadata.ProjectIdentity == "" {
		metadata.ProjectIdentity = projectIdentity(metadata.ProjectRoot)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteError("begin session run", err)
	}
	defer rollbackTransaction(tx, &resultErr, "rollback session run")
	stamp := formatTime(metadata.StartedAt)
	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO sessions(
    id, project_root, project_identity, title, status, last_seq, snapshot_seq, created_at, updated_at
) VALUES (?, ?, ?, NULL, 'active', 0, 0, ?, ?)`,
		metadata.SessionID, metadata.ProjectRoot, metadata.ProjectIdentity, stamp, stamp,
	)
	if err != nil {
		return classifySQLiteError("create session", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect session insert: %w", err)
	}
	if inserted == 0 {
		var root, identity string
		if err := tx.QueryRowContext(ctx,
			`SELECT project_root, project_identity FROM sessions WHERE id = ?`, metadata.SessionID,
		).Scan(&root, &identity); err != nil {
			return classifySQLiteError("load existing session", err)
		}
		if root != metadata.ProjectRoot || identity != metadata.ProjectIdentity {
			return ErrRunConflict
		}
	}
	result, err = tx.ExecContext(ctx, `
INSERT OR IGNORE INTO runs(
    id, session_id, command_id, status, provider, model, last_run_seq, started_at, finished_at
) VALUES (?, ?, NULL, 'running', ?, ?, 0, ?, NULL)`,
		metadata.RunID, metadata.SessionID, metadata.Provider, metadata.Model, stamp,
	)
	if err != nil {
		return classifySQLiteError("create session run", err)
	}
	inserted, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect run insert: %w", err)
	}
	if inserted == 0 {
		var sessionID, provider, model string
		if err := tx.QueryRowContext(ctx,
			`SELECT session_id, provider, model FROM runs WHERE id = ?`, metadata.RunID,
		).Scan(&sessionID, &provider, &model); err != nil {
			return classifySQLiteError("load existing run", err)
		}
		if sessionID != metadata.SessionID || provider != metadata.Provider || model != metadata.Model {
			return ErrRunConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return classifySQLiteError("commit session run", err)
	}
	return nil
}

func validateRunMetadata(metadata RunMetadata) error {
	switch {
	case strings.TrimSpace(metadata.SessionID) == "":
		return errors.New("session id is required")
	case len(metadata.SessionID) > 256:
		return errors.New("session id exceeds 256 bytes")
	case strings.TrimSpace(metadata.RunID) == "":
		return errors.New("run id is required")
	case len(metadata.RunID) > 256:
		return errors.New("run id exceeds 256 bytes")
	case strings.TrimSpace(metadata.ProjectRoot) == "":
		return errors.New("run project root is required")
	case !filepath.IsAbs(metadata.ProjectRoot):
		return errors.New("run project root must be absolute")
	case strings.TrimSpace(metadata.Provider) == "":
		return errors.New("run provider is required")
	case len(metadata.Provider) > 256:
		return errors.New("run provider exceeds 256 bytes")
	case strings.TrimSpace(metadata.Model) == "":
		return errors.New("run model is required")
	case len(metadata.Model) > 256:
		return errors.New("run model exceeds 256 bytes")
	case len(metadata.ProjectIdentity) > 256:
		return errors.New("project identity exceeds 256 bytes")
	case metadata.StartedAt.IsZero():
		return errors.New("run start time is required")
	default:
		return nil
	}
}

func projectIdentity(root string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(root)))
	return fmt.Sprintf("sha256:%x", digest[:])
}

// Append atomically compares the session position, inserts a bounded record
// batch, and advances denormalized session/run indexes.
func (s *SQLiteStore) Append(ctx context.Context, sessionID string, expectedSeq uint64, records []Record) (resultErr error) {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("append session id is required")
	}
	if len(records) == 0 {
		return errors.New("append requires at least one session record")
	}
	if len(records) > maxBatchRecords {
		return fmt.Errorf("append exceeds %d records", maxBatchRecords)
	}
	if expectedSeq > ^uint64(0)-uint64(len(records)) {
		return errors.New("append session sequence overflows uint64")
	}
	seenIDs := make(map[string]struct{}, len(records))
	runLastSeq := make(map[string]uint64)
	terminalRuns := make(map[string]struct{})
	for index, record := range records {
		if err := record.Validate(); err != nil {
			return fmt.Errorf("validate record %d: %w", index, err)
		}
		if record.SessionID != sessionID {
			return fmt.Errorf("record %d session_id does not match append session", index)
		}
		want := expectedSeq + uint64(index) + 1
		if record.SessionSeq != want {
			return fmt.Errorf("record %d session_seq=%d, want %d", index, record.SessionSeq, want)
		}
		if _, duplicate := seenIDs[record.ID]; duplicate {
			return fmt.Errorf("%w: %s appears twice in batch", ErrDuplicateEvent, record.ID)
		}
		seenIDs[record.ID] = struct{}{}
		if _, terminal := terminalRuns[record.RunID]; terminal {
			return fmt.Errorf("record %d follows a terminal event for run %q", index, record.RunID)
		}
		if previous := runLastSeq[record.RunID]; previous != 0 && record.RunSeq <= previous {
			return fmt.Errorf("record %d run_seq=%d is not greater than %d", index, record.RunSeq, previous)
		}
		runLastSeq[record.RunID] = record.RunSeq
		if terminalStatus(record.Kind) != "" {
			terminalRuns[record.RunID] = struct{}{}
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteError("begin append", err)
	}
	defer rollbackTransaction(tx, &resultErr, "rollback session append")
	lastSeq := records[len(records)-1].SessionSeq
	updatedAt := formatTime(s.now().UTC())
	result, err := tx.ExecContext(ctx,
		`UPDATE sessions SET last_seq = ?, updated_at = ? WHERE id = ? AND last_seq = ?`,
		lastSeq, updatedAt, sessionID, expectedSeq,
	)
	if err != nil {
		return classifySQLiteError("claim session sequence", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect session sequence update: %w", err)
	}
	if updated == 0 {
		var actual uint64
		if err := tx.QueryRowContext(ctx, `SELECT last_seq FROM sessions WHERE id = ?`, sessionID).Scan(&actual); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrSessionNotFound
			}
			return classifySQLiteError("load session sequence", err)
		}
		return &SequenceConflictError{Expected: expectedSeq, Actual: actual}
	}

	runStatus := make(map[string]string)
	sessionStatus := ""
	for _, record := range records {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO events(
    event_id, session_id, session_seq, run_id, run_seq, command_id,
    kind, schema_version, visibility, payload_json, created_at
) VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)`,
			record.ID, record.SessionID, record.SessionSeq, record.RunID, record.RunSeq, record.CommandID,
			record.Kind, record.SchemaVersion, string(record.Visibility), []byte(record.Payload), formatTime(record.Time),
		); err != nil {
			return classifySQLiteError("insert session event", err)
		}
		if record.RunSeq > runLastSeq[record.RunID] {
			runLastSeq[record.RunID] = record.RunSeq
		}
		if status := terminalStatus(record.Kind); status != "" {
			runStatus[record.RunID] = status
			sessionStatus = status
		}
	}
	runIDs := make([]string, 0, len(runLastSeq))
	for runID := range runLastSeq {
		runIDs = append(runIDs, runID)
	}
	sort.Strings(runIDs)
	for _, runID := range runIDs {
		status := runStatus[runID]
		if status == "" {
			result, err = tx.ExecContext(ctx,
				`UPDATE runs SET last_run_seq = ? WHERE id = ? AND session_id = ? AND status = 'running' AND last_run_seq < ?`,
				runLastSeq[runID], runID, sessionID, runLastSeq[runID],
			)
		} else {
			result, err = tx.ExecContext(ctx,
				`UPDATE runs SET last_run_seq = ?, status = ?, finished_at = ? WHERE id = ? AND session_id = ? AND status = 'running' AND last_run_seq < ?`,
				runLastSeq[runID], status, updatedAt, runID, sessionID, runLastSeq[runID],
			)
		}
		if err != nil {
			return classifySQLiteError("update session run", err)
		}
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return fmt.Errorf("inspect run update: %w", countErr)
		}
		if count != 1 {
			return ErrRunConflict
		}
	}
	if sessionStatus != "" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET status = ?, updated_at = ? WHERE id = ?`, sessionStatus, updatedAt, sessionID,
		); err != nil {
			return classifySQLiteError("update session status", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return classifySQLiteError("commit append", err)
	}
	return nil
}

func terminalStatus(kind string) string {
	switch kind {
	case "run.completed":
		return "completed"
	case "run.failed":
		return "failed"
	case "run.cancelled":
		return "cancelled"
	case "run.interrupted":
		return "interrupted"
	default:
		return ""
	}
}

// Load returns records after one session sequence in durable order.
func (s *SQLiteStore) Load(ctx context.Context, sessionID string, afterSeq uint64, limit int) (result []Record, resultErr error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("load session id is required")
	}
	if limit == 0 {
		limit = defaultLoadLimit
	}
	if limit < 0 || limit > maximumLoadLimit {
		return nil, fmt.Errorf("load limit must be between 1 and %d", maximumLoadLimit)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT event_id, session_id, session_seq, run_id, run_seq, COALESCE(command_id, ''),
       kind, schema_version, visibility, payload_json, created_at
FROM events
WHERE session_id = ? AND session_seq > ?
ORDER BY session_seq
LIMIT ?`, sessionID, afterSeq, limit)
	if err != nil {
		return nil, classifySQLiteError("load session events", err)
	}
	defer closeRows(rows, &resultErr, "close session event rows")
	result = make([]Record, 0)
	for rows.Next() {
		var record Record
		var visibility string
		var payload []byte
		var createdAt string
		if err := rows.Scan(
			&record.ID, &record.SessionID, &record.SessionSeq, &record.RunID, &record.RunSeq, &record.CommandID,
			&record.Kind, &record.SchemaVersion, &visibility, &payload, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan session event: %w", err)
		}
		record.Visibility = Visibility(visibility)
		record.Payload = append(json.RawMessage(nil), payload...)
		record.Time, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("decode session event time: %w", err)
		}
		if err := record.Validate(); err != nil {
			return nil, fmt.Errorf("decode session event %q: %w", record.ID, err)
		}
		result = append(result, cloneRecord(record))
	}
	if err := rows.Err(); err != nil {
		return nil, classifySQLiteError("iterate session events", err)
	}
	return result, nil
}

// Path returns the local database path for diagnostics without exposing its
// contents.
func (s *SQLiteStore) Path() string { return s.path }

// Close releases SQLite and its WAL handles.
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close session database: %w", err)
	}
	return nil
}

func closeDatabaseAfterError(db *sql.DB, cause error) error {
	if closeErr := db.Close(); closeErr != nil {
		return errors.Join(cause, fmt.Errorf("close session database after failure: %w", closeErr))
	}
	return cause
}

func rollbackTransaction(tx *sql.Tx, resultErr *error, operation string) {
	if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		*resultErr = errors.Join(*resultErr, fmt.Errorf("%s: %w", operation, rollbackErr))
	}
}

func closeRows(rows *sql.Rows, resultErr *error, operation string) {
	if closeErr := rows.Close(); closeErr != nil {
		*resultErr = errors.Join(*resultErr, fmt.Errorf("%s: %w", operation, closeErr))
	}
}

type sqliteCoder interface{ Code() int }

func classifySQLiteError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	var coded sqliteCoder
	if errors.As(err, &coded) {
		switch coded.Code() & 0xff {
		case 5, 6: // SQLITE_BUSY / SQLITE_LOCKED
			return fmt.Errorf("%s: %w: %v", operation, ErrStoreBusy, err)
		case 13: // SQLITE_FULL
			return fmt.Errorf("%s: %w: %v", operation, ErrStoreFull, err)
		case 19: // SQLITE_CONSTRAINT and extended forms
			if operation != "insert session event" {
				break
			}
			return fmt.Errorf("%s: %w: %v", operation, ErrDuplicateEvent, err)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// Ensure compile-time conformance at the storage boundary.
var _ EventStore = (*SQLiteStore)(nil)
