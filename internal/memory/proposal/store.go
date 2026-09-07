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

// insertApplyResultSQL writes a single row to proposal_applies. The
// bind list is laid out in the same order as the migration
// 011_proposal_applies.sql column list so diffing the migration against
// the Go bind list is mechanical. The apply row id is generated from
// (now, kind, proposalID+":apply") so the audit trail stays
// deterministic without colliding across proposals.
const insertApplyResultSQL = `INSERT INTO proposal_applies
    (id, proposal_id, kind, target, result, error, applied_at, patch_id)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

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

// Apply runs the kind-routed side effect for an approved proposal and
// records the outcome in proposal_applies. The flow is:
//
//  1. Load the proposal; ErrProposalNotFound if missing.
//  2. Reject anything other than StatusApproved — the executor is
//     never invoked on a not-approved row, and no proposal_applies
//     row is written (proposal_applies.proposal_id is UNIQUE, so we
//     never want half-applied state in the table).
//  3. Idempotent guard: if a proposal_applies row already exists for
//     this proposal_id, return it unchanged and skip the executor.
//     proposal_id is UNIQUE so the second insert would otherwise
//     error and we save the caller from a double side-effect (memory
//     row patched twice, AGENTS.md rewritten twice, ...).
//  4. Dispatch by Kind to the matching ApplyExecutor method. Unknown
//     Kind values map to ErrProposalNotApplicable.
//  5. Stamp ProposalID / Kind / AppliedAt when the executor leaves
//     them empty so every audit row carries the provenance.
//  6. Persist the ApplyResult via insertApplyResult and return it.
//
// The executor's error is propagated as-is so callers can branch on
// it with errors.Is; a missing executor method (unknown Kind) maps to
// ErrProposalNotApplicable rather than a bare fmt.Errorf so the CLI
// can map it to a distinct exit code.
func (s *Store) Apply(ctx context.Context, proposalID string, executor ApplyExecutor) (ApplyResult, error) {
	p, err := s.Get(ctx, proposalID)
	if err != nil {
		return ApplyResult{}, err
	}
	if p.Status != StatusApproved {
		return ApplyResult{}, fmt.Errorf("%w: %s is %s, not approved",
			ErrProposalNotApplicable, proposalID, p.Status)
	}

	// Idempotent guard — a second Apply for the same proposal_id
	// short-circuits with the existing record. ErrProposalNotFound
	// from getApplyResult means no row exists, which is the expected
	// first-call outcome, so it is intentionally ignored.
	if existing, gerr := s.getApplyResult(ctx, proposalID); gerr == nil && !existing.AppliedAt.IsZero() {
		return existing, nil
	}

	var (
		result ApplyResult
		rerr   error
	)
	switch p.Kind {
	case KindMemoryUpgrade:
		result, rerr = executor.ApplyMemoryUpgrade(ctx, p)
	case KindAgentsMdRevision:
		result, rerr = executor.ApplyAgentsMdRevision(ctx, p)
	case KindSkillDraft:
		result, rerr = executor.ApplySkillDraft(ctx, p)
	case KindObsolete:
		result, rerr = executor.ApplyObsolete(ctx, p)
	default:
		return ApplyResult{}, fmt.Errorf("%w: unknown kind %s", ErrProposalNotApplicable, p.Kind)
	}
	if rerr != nil {
		return ApplyResult{}, rerr
	}
	if result.ProposalID == "" {
		result.ProposalID = proposalID
	}
	if result.Kind == "" {
		result.Kind = p.Kind
	}
	if result.AppliedAt.IsZero() {
		result.AppliedAt = s.now()
	}
	if ierr := s.insertApplyResult(ctx, result); ierr != nil {
		return ApplyResult{}, fmt.Errorf("record apply result: %w", ierr)
	}
	return result, nil
}

// Revert marks the apply audit row as reverted. v0.2 audit-only —
// does NOT undo the actual side effect (memory row patched,
// AGENTS.md rewritten, archive committed, file written). The actual
// rollback is a v0.3 follow-up. Callers branch on RevertedAt to
// decide whether the audit row has been marked reverted; the CLI
// (Task 2) uses ErrProposalNotApplied / ErrProposalAlreadyReverted
// to map missing / double-revert to distinct exit codes via errors.Is.
//
// Flow:
//
//  1. Check the proposal_applies row exists; ErrProposalNotApplied
//     when missing — the proposal never reached Store.Apply so
//     there is nothing to mark reverted.
//  2. Check reverted_at is NULL; ErrProposalAlreadyReverted when
//     the marker is already set — a second Revert is a no-op so
//     the caller learns the prior reviewer via getApplyResult
//     rather than silently overwriting the stamp.
//  3. UPDATE … WHERE reverted_at IS NULL atomically so a concurrent
//     Revert on the same id cannot double-stamp the marker. The
//     RowsAffected == 0 branch surfaces as ErrProposalAlreadyReverted
//     to keep the public contract identical to step 2 even under
//     racing callers.
//  4. Re-fetch via getApplyResult so the returned ApplyResult
//     carries RevertedAt + Reviewer populated from the same row.
func (s *Store) Revert(ctx context.Context, proposalID, reviewer string) (ApplyResult, error) {
	var revertedAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT reverted_at FROM proposal_applies WHERE proposal_id = ?`, proposalID,
	).Scan(&revertedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ApplyResult{}, fmt.Errorf("%w: %s", ErrProposalNotApplied, proposalID)
	}
	if err != nil {
		return ApplyResult{}, err
	}
	if revertedAt.Valid && revertedAt.String != "" {
		return ApplyResult{}, fmt.Errorf("%w: %s", ErrProposalAlreadyReverted, proposalID)
	}

	stamp := formatStamp(s.now().UTC())
	res, err := s.db.ExecContext(ctx,
		`UPDATE proposal_applies SET reverted_at = ?, reverted_by = ? WHERE proposal_id = ? AND reverted_at IS NULL`,
		stamp, reviewer, proposalID,
	)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("revert proposal apply: %w", err)
	}
	if rows, rerr := res.RowsAffected(); rerr != nil {
		return ApplyResult{}, fmt.Errorf("revert rows affected: %w", rerr)
	} else if rows == 0 {
		return ApplyResult{}, fmt.Errorf("%w: %s", ErrProposalAlreadyReverted, proposalID)
	}
	return s.getApplyResult(ctx, proposalID)
}

// getApplyResult returns the proposal_applies row for proposalID, or
// ErrProposalNotFound when no row exists. The apply row's own id
// column is discarded (the ApplyResult value type does not surface
// it); proposal_id UNIQUE means the result is at most one row. Errors
// from the parser mirror scanProposalFields: parse failures on
// applied_at silently leave the timestamp zero so the caller can
// distinguish "stored row" (AppliedAt != zero) from "no row".
//
// Migration 012 added reverted_at / reverted_by; both surface as
// ApplyResult.RevertedAt (nil when un-reverted) and
// ApplyResult.Reviewer (mirrors reverted_by). Parse failures on
// reverted_at silently leave the marker zero so a malformed marker
// does not cascade into a failed read.
func (s *Store) getApplyResult(ctx context.Context, proposalID string) (ApplyResult, error) {
	var (
		r          ApplyResult
		rowID      string
		errMsg     sql.NullString
		patchID    sql.NullString
		revertedAt sql.NullString
		revertedBy sql.NullString
		appliedAt  string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, proposal_id, kind, target, result, error, applied_at, patch_id, reverted_at, reverted_by
		 FROM proposal_applies WHERE proposal_id = ?`, proposalID,
	).Scan(&rowID, &r.ProposalID, &r.Kind, &r.Target, &r.Result, &errMsg, &appliedAt, &patchID, &revertedAt, &revertedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return ApplyResult{}, ErrProposalNotFound
	}
	if err != nil {
		return ApplyResult{}, err
	}
	r.Error = errMsg.String
	r.PatchID = patchID.String
	if t, perr := time.Parse(time.RFC3339Nano, appliedAt); perr == nil {
		r.AppliedAt = t
	}
	if revertedAt.Valid && revertedAt.String != "" {
		if t, perr := time.Parse(time.RFC3339Nano, revertedAt.String); perr == nil {
			r.RevertedAt = &t
		}
	}
	r.Reviewer = revertedBy.String
	return r, nil
}

// insertApplyResult writes r to proposal_applies. The apply row id is
// generated via generateProposalID with the ":apply" suffix so the
// audit row id is distinguishable from the proposal row id without a
// separate prefix table; error / patch_id use nullString so empty
// strings land as SQL NULL rather than empty TEXT.
func (s *Store) insertApplyResult(ctx context.Context, r ApplyResult) error {
	applyID := generateProposalID(s.now(), r.Kind, r.ProposalID+":apply")
	_, err := s.db.ExecContext(ctx, insertApplyResultSQL,
		applyID, r.ProposalID, string(r.Kind), r.Target, r.Result, nullString(r.Error),
		formatStamp(r.AppliedAt.UTC()), nullString(r.PatchID),
	)
	return err
}
