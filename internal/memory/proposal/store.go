// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package proposal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// List bounds for Store.List. Limit defaults to listDefaultLimit when zero
// and is capped at listMaxLimit; values outside the range produce
// ErrInvalidQuery so callers can branch on it with errors.Is.
const (
	listDefaultLimit = 20
	listMaxLimit     = 200
)

// ErrInvalidQuery is returned by List when q.Limit is outside the
// [0, listMaxLimit] window. Mirrors the memory package's sentinel so
// callers can branch on validation vs routing failures.
var ErrInvalidQuery = errors.New("invalid proposal query")

// proposalColumnsSelect is the canonical column projection shared by every
// read-side query. Keeping it in one place ensures Get / List / future
// loaders all see the same shape scanProposalFields expects.
const proposalColumnsSelect = `SELECT id, kind, title, body, status, based_on, session_id, confidence,
       evidence, observed_at, reviewed_at, reviewer, created_at, updated_at`

// insertProposalSQL is the canonical INSERT projection. The 14 placeholders
// match scanProposalFields scan order; the bind list is laid out in the
// same order as the migration 010_reflection_proposals.sql column list so
// diffing the migration against the Go bind list is mechanical.
const insertProposalSQL = `INSERT INTO reflection_proposals
    (id, kind, title, body, status, based_on, session_id, confidence,
     evidence, observed_at, reviewed_at, reviewer, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// Store is the proposal storage facade over the session-owned SQLite
// database. It borrows the connection via session.SQLiteStore.DB() and
// shares the 010_reflection_proposals migration installed by OpenSQLite.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open returns a Store backed by the given session-owned *sql.DB. The store
// assumes the 010_reflection_proposals schema is already applied —
// OpenSQLite takes care of that — and reuses the connection pool.
func Open(db *sql.DB, now func() time.Time) *Store {
	return &Store{db: db, now: now}
}

// Insert writes p to reflection_proposals and returns the durable row with
// ID / CreatedAt / UpdatedAt filled in. The three JSON columns are
// marshaled up front so a partial row never lands; nil slices are coerced
// to empty so the NOT NULL TEXT columns never see a JSON null.
//
// When p.ID is empty a deterministic id is generated via generateProposalID
// (mirrors memory.GenerateID's sha256-based wire value but with a
// time-stable prefix so the Reflect Worker can emit many distinct rows
// without colliding). Caller-supplied IDs are accepted so future Pipeline
// stages can carry an id through retry paths.
func (s *Store) Insert(ctx context.Context, p Proposal) (Proposal, error) {
	if p.Kind == "" || strings.TrimSpace(p.Title) == "" {
		return Proposal{}, fmt.Errorf("%w: kind and title are required", ErrInvalidProposal)
	}

	// Coerce nil slices to empty so the NOT NULL JSON TEXT columns never
	// see a JSON null (Marshal renders nil slice as "null", which would
	// violate the schema).
	if p.BasedOn == nil {
		p.BasedOn = []string{}
	}
	if p.Evidence == nil {
		p.Evidence = []Evidence{}
	}

	bodyJSON, err := json.Marshal(p.Body)
	if err != nil {
		return Proposal{}, fmt.Errorf("marshal proposal body: %w", err)
	}
	basedOnJSON, err := json.Marshal(p.BasedOn)
	if err != nil {
		return Proposal{}, fmt.Errorf("marshal proposal based_on: %w", err)
	}
	evidenceJSON, err := json.Marshal(p.Evidence)
	if err != nil {
		return Proposal{}, fmt.Errorf("marshal proposal evidence: %w", err)
	}

	now := s.now().UTC()
	if p.ObservedAt.IsZero() {
		p.ObservedAt = now
	}
	if p.ID == "" {
		p.ID = generateProposalID(now, p.Kind, p.Title)
	}
	stamp := formatStamp(now)
	observedStamp := formatStamp(p.ObservedAt.UTC())

	if _, err := s.db.ExecContext(ctx, insertProposalSQL,
		p.ID, string(p.Kind), p.Title, string(bodyJSON), string(p.Status),
		string(basedOnJSON), nullString(p.SessionID), p.Confidence, string(evidenceJSON),
		observedStamp, nil, nil, stamp, stamp,
	); err != nil {
		return Proposal{}, fmt.Errorf("insert proposal: %w", err)
	}
	p.CreatedAt = now
	p.UpdatedAt = now
	return p, nil
}

// Get returns the single proposal row for id, or ErrProposalNotFound
// (wrapped with the id) if no such row exists. It is a read-only lookup
// that does not open a write transaction.
func (s *Store) Get(ctx context.Context, id string) (Proposal, error) {
	row := s.db.QueryRowContext(ctx, proposalColumnsSelect+` FROM reflection_proposals WHERE id = ?`, id)
	p, err := scanProposalFields(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, fmt.Errorf("%w: %s", ErrProposalNotFound, id)
	}
	if err != nil {
		return Proposal{}, fmt.Errorf("load proposal %s: %w", id, err)
	}
	return p, nil
}

// List returns proposals matching the dynamic WHERE clause built from q.
// Empty filter fields mean "no constraint on that column"; an empty q
// matches every row. Ordering is observed_at DESC so the review queue is
// newest-first. Limit defaults to listDefaultLimit and is clamped at
// listMaxLimit; q.Limit < 0 or q.Limit > listMaxLimit returns ErrInvalidQuery.
func (s *Store) List(ctx context.Context, q ListQuery) ([]Proposal, error) {
	if q.Limit < 0 {
		return nil, fmt.Errorf("%w: limit %d must be >= 0", ErrInvalidQuery, q.Limit)
	}
	if q.Limit > listMaxLimit {
		return nil, fmt.Errorf("%w: limit %d exceeds max %d", ErrInvalidQuery, q.Limit, listMaxLimit)
	}
	limit := q.Limit
	if limit == 0 {
		limit = listDefaultLimit
	}

	var (
		clauses []string
		args    []any
	)
	if q.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, string(q.Status))
	}
	if q.Kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, string(q.Kind))
	}
	if q.SessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, q.SessionID)
	}
	if !q.Since.IsZero() {
		clauses = append(clauses, "observed_at >= ?")
		args = append(args, formatStamp(q.Since.UTC()))
	}

	query := proposalColumnsSelect + " FROM reflection_proposals"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	// v0.1 always orders observed_at DESC; OrderBy is reserved for v0.2
	// sort hints so the CLI can wire them up without a schema bump.
	query += " ORDER BY observed_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]Proposal, 0, limit)
	for rows.Next() {
		p, err := scanProposalFields(rows)
		if err != nil {
			return nil, fmt.Errorf("scan proposal row: %w", err)
		}
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proposal rows: %w", err)
	}
	return result, nil
}

// UpdateStatus flips a proposal from proposed to status (approved or
// rejected), stamps reviewer + reviewed_at, and bumps updated_at. Returns
// ErrProposalNotFound when no row matches the id so the CLI can map it to
// a distinct exit code via errors.Is.
//
// UpdateStatus does NOT touch memory / AGENTS.md / Skills — it is the
// review step only. Apply / dispatch (Tasks 2 and 3) reads status='approved'
// rows and routes them by Kind.
func (s *Store) UpdateStatus(ctx context.Context, id string, status ProposalStatus, reviewer string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: id is required", ErrProposalNotFound)
	}
	stamp := formatStamp(s.now().UTC())
	res, err := s.db.ExecContext(ctx,
		`UPDATE reflection_proposals SET status=?, reviewer=?, reviewed_at=?, updated_at=? WHERE id=?`,
		string(status), reviewer, stamp, stamp, id,
	)
	if err != nil {
		return fmt.Errorf("update proposal status: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: %s", ErrProposalNotFound, id)
	}
	return nil
}

// rowScanner abstracts over *sql.Row and *sql.Rows so a single helper can
// parse the reflection_proposals projection regardless of whether the
// caller holds a single-row result (Get) or is iterating rows (List).
type rowScanner interface {
	Scan(dest ...any) error
}

// scanProposalFields reads one row produced by proposalColumnsSelect and
// decodes it back into the Proposal value type, including the JSON
// marshalled columns (body / based_on / evidence) and the RFC3339Nano
// timestamp columns the Store layer persists as strings.
func scanProposalFields(scanner rowScanner) (Proposal, error) {
	var (
		p            Proposal
		kind         string
		status       string
		bodyJSON     string
		basedOnJSON  string
		evidenceJSON string
		sessionID    sql.NullString
		reviewer     sql.NullString
		reviewedAt   sql.NullString
		observedAt   string
		createdAt    string
		updatedAt    string
	)
	if err := scanner.Scan(
		&p.ID, &kind, &p.Title, &bodyJSON, &status, &basedOnJSON,
		&sessionID, &p.Confidence, &evidenceJSON,
		&observedAt, &reviewedAt, &reviewer, &createdAt, &updatedAt,
	); err != nil {
		return Proposal{}, err
	}
	p.Kind = ProposalKind(kind)
	p.Status = ProposalStatus(status)
	if sessionID.Valid {
		p.SessionID = sessionID.String
	}
	if reviewer.Valid {
		p.Reviewer = reviewer.String
	}
	if err := json.Unmarshal([]byte(bodyJSON), &p.Body); err != nil {
		return Proposal{}, fmt.Errorf("unmarshal proposal body: %w", err)
	}
	if err := json.Unmarshal([]byte(basedOnJSON), &p.BasedOn); err != nil {
		return Proposal{}, fmt.Errorf("unmarshal proposal based_on: %w", err)
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &p.Evidence); err != nil {
		return Proposal{}, fmt.Errorf("unmarshal proposal evidence: %w", err)
	}
	if t, err := time.Parse(time.RFC3339Nano, observedAt); err == nil {
		p.ObservedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		p.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		p.UpdatedAt = t
	}
	if reviewedAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, reviewedAt.String); err == nil {
			p.ReviewedAt = &t
		}
	}
	return p, nil
}

// generateProposalID returns a sha256-derived id with the prop_ prefix,
// mirroring memory.GenerateID's wire shape (mem_ prefix, 16-byte hex
// suffix) but seeded from (now, kind, title) so the Reflect Worker can
// emit many distinct rows without colliding. The unix-nano suffix
// disambiguates two proposals emitted in the same millisecond.
func generateProposalID(now time.Time, kind ProposalKind, title string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%d", now.UTC().Format(time.RFC3339Nano), kind, title, now.UnixNano())
	return "prop_" + hex.EncodeToString(h.Sum(nil))[:16]
}

// formatStamp mirrors memory.formatStamp and session.formatTime:
// RFC3339Nano in UTC. Inlined so callers reading store.go see the wire
// format at a glance without chasing an exported helper.
func formatStamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// nullString returns nil when s is empty so the bound parameter stores
// SQL NULL instead of an empty string. Empty TEXT and NULL TEXT differ at
// the scanner (sql.NullString.Valid is false for NULL, true for "") so the
// distinction is observable downstream.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
