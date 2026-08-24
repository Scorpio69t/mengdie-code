// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package memory 的 Store 层负责把 Authority 守门、idempotency、conflict 标记
// 与物理 schema 绑定在一起；本文件只暴露 Save 入口族与 OpenMemory 工厂，具
// 体的 List/Get/Why/Forget/Supersede/Approve/RecordEvidence/RecordUsage/
// RecomputeEvidenceScore 留待后续 Task。
//
// 实现对应规范 §4.1（Authority 写入守门）与 §4.2（冲突策略）：
//
//   - Authority 与 SourceType 的绑定关系在 Go 层强制一次，避免静默错把
//     agent_message 写成 active 的 explicit memory；
//   - 同 scope + 规范化后同 claim 的 memory 视作同一条（idempotency）；
//   - 同 scope + 同 authority + 不同 claim 时双方都置 disputed（新行随
//     peer 一起翻转，让 `mengdie memory why <id>` 能给出冲突链）；
//   - 写采用 INSERT ... ON CONFLICT(id) DO NOTHING RETURNING id 模式，
//     两个并发写不会因为 SELECT-then-INSERT 之间出现 UNIQUE 冲突。
package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// Sentinel errors returned by Store. Callers use errors.Is to branch on
// validation vs routing failures.
var (
	// ErrInvalidMemory reports a malformed Memory (missing Scope or SourceRef,
	// unsupported Authority wire value, etc).
	ErrInvalidMemory = errors.New("invalid memory")
	// ErrAuthorityGuard reports a Save* call whose Source.Type does not match
	// the Authority pairing required by spec §4.1.
	ErrAuthorityGuard = errors.New("memory authority guard violation")
	// ErrInvalidQuery reports a malformed ListQuery (limit out of [0, 200]
	// range per spec §5 CLI list limit).
	ErrInvalidQuery = errors.New("invalid memory query")
	// ErrMemoryNotFound is returned by Get / Why when no row exists for the
	// requested id. CLI exit code 3 per spec §5 maps to this sentinel.
	ErrMemoryNotFound = errors.New("memory not found")
)

// Memory list bounds per spec §5 (`mengdie memory list --limit N`).
const (
	listDefaultLimit = 20
	listMaxLimit     = 200
)

// ListQuery is the filter / pagination surface for Store.List. Empty fields
// mean "no constraint on that column" so the dynamic WHERE clause only adds
// filters the caller actually supplied. Limit defaults to 20 and is capped at
// 200 per spec §5; values outside [0, 200] produce ErrInvalidQuery so callers
// can branch on it with errors.Is.
type ListQuery struct {
	ScopeKind  string
	ScopeValue string
	Authority  string
	Status     string
	Limit      int
}

// WhyReport is the audit surface for `mengdie memory why <id>` (spec §5) and
// the Trust Set's why_completeness metric (spec §7). It carries all six
// sections the spec lists — source, observed_at, scope, evidence, conflicts,
// recent_usage — in a stable layout the CLI formatter can render
// unconditionally. Memory embeds the first three sections (Source, ObservedAt,
// Scope) while the Source field is duplicated for the spec §5 "原始来源"
// heading without dereferencing Memory. The slice fields are always
// non-nil so the CLI can range over them without nil checks.
type WhyReport struct {
	Memory      Memory
	Source      SourceRef
	Evidence    []Evidence
	Conflicts   []Memory
	RecentUsage []UsageRecord
}

// Store is the trusted-memory facade over the session-owned SQLite database.
// It borrows the connection via session.SQLiteStore.DB() and shares the
// 008_memory migration installed by OpenSQLite.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// OpenMemory returns a Store backed by the session SQLite database. The store
// assumes the 008_memory schema is already applied — OpenSQLite takes care of
// that — and reuses its connection pool.
func OpenMemory(sessionStore *session.SQLiteStore) *Store {
	return &Store{db: sessionStore.DB(), now: time.Now}
}

// SetNow overrides the clock used to stamp created_at / updated_at and the
// observed_at fallback. Tests use it for deterministic comparisons.
func (s *Store) SetNow(now func() time.Time) { s.now = now }

// Save routes m to the Save* method that matches m.Authority per spec §4.1.
// Unrecognised Authority values fall through to ErrInvalidMemory.
func (s *Store) Save(ctx context.Context, m Memory) (Memory, error) {
	switch m.Authority {
	case AuthorityExplicit:
		return s.SaveUserMemory(ctx, m)
	case AuthorityRepository:
		return s.SaveRepositoryFact(ctx, m)
	case AuthorityVerified:
		return s.SaveVerifiedFact(ctx, m)
	case AuthorityInferred:
		return s.ProposeMemory(ctx, m)
	default:
		return Memory{}, fmt.Errorf("%w: unsupported authority %q", ErrInvalidMemory, m.Authority)
	}
}

// SaveUserMemory writes an explicit user-stated memory with status=active.
// Authority must be (or become) AuthorityExplicit and Source.Type must be
// (or become) SourceTypeUserMessage.
func (s *Store) SaveUserMemory(ctx context.Context, m Memory) (Memory, error) {
	return s.guardSave(ctx, m, AuthorityExplicit, SourceTypeUserMessage, StatusActive)
}

// SaveRepositoryFact writes a repository-derived fact with status=active.
func (s *Store) SaveRepositoryFact(ctx context.Context, m Memory) (Memory, error) {
	return s.guardSave(ctx, m, AuthorityRepository, SourceTypeFile, StatusActive)
}

// SaveVerifiedFact writes a test/build/lint-verified fact with status=active.
func (s *Store) SaveVerifiedFact(ctx context.Context, m Memory) (Memory, error) {
	return s.guardSave(ctx, m, AuthorityVerified, SourceTypeCommandResult, StatusActive)
}

// ProposeMemory writes an agent-inferred memory with status=proposed. The
// memory stays proposed until `mengdie memory approve <id>` is called.
func (s *Store) ProposeMemory(ctx context.Context, m Memory) (Memory, error) {
	return s.guardSave(ctx, m, AuthorityInferred, SourceTypeAgentMessage, StatusProposed)
}

// guardSave applies the spec §4.1 Authority↔SourceType pairing and delegates
// to save for the canonical idempotent + conflict-aware insert.
func (s *Store) guardSave(ctx context.Context, m Memory, authority Authority, source SourceType, status Status) (Memory, error) {
	// Caller-passed Authority must agree if both sides are set; we always
	// rewrite it to the routed value to keep the wire value consistent with
	// the Save* entry point the caller chose.
	if m.Authority != "" && m.Authority != authority {
		return Memory{}, fmt.Errorf("%w: %s authority=%q, want %q", ErrAuthorityGuard, methodForAuthority(authority), m.Authority, authority)
	}
	m.Authority = authority
	if m.Source.Type != "" && m.Source.Type != source {
		return Memory{}, fmt.Errorf("%w: %s source_type=%q, want %q", ErrAuthorityGuard, methodForAuthority(authority), m.Source.Type, source)
	}
	m.Source.Type = source
	m.Status = status
	return s.save(ctx, m)
}

// methodForAuthority returns the public Save* method name that owns the given
// Authority. Used to label ErrAuthorityGuard messages.
func methodForAuthority(authority Authority) string {
	switch authority {
	case AuthorityExplicit:
		return "SaveUserMemory"
	case AuthorityRepository:
		return "SaveRepositoryFact"
	case AuthorityVerified:
		return "SaveVerifiedFact"
	case AuthorityInferred:
		return "ProposeMemory"
	default:
		return "Save"
	}
}

// save performs the canonical idempotency + conflict + insert flow shared by
// every public Save* method. It owns its own transaction so the normalized
// SELECT-for-idempotency runs against a consistent snapshot, and the
// subsequent INSERT relies on ON CONFLICT(id) DO NOTHING so two concurrent
// writers with the same (claim, scope, authority, sessionID) tuple cannot
// surface a UNIQUE-constraint error to the caller.
func (s *Store) save(ctx context.Context, m Memory) (Memory, error) {
	if err := m.Scope.Valid(); err != nil {
		return Memory{}, fmt.Errorf("%w: %v", ErrInvalidMemory, err)
	}
	if err := m.Source.Valid(); err != nil {
		return Memory{}, fmt.Errorf("%w: %v", ErrInvalidMemory, err)
	}
	if strings.TrimSpace(m.Claim) == "" {
		return Memory{}, fmt.Errorf("%w: claim is required", ErrInvalidMemory)
	}
	if m.Kind == "" {
		m.Kind = "fact"
	}

	sessionID := sessionIDFromSource(m.Source.Ref)
	normalized := normalizeClaim(m.Claim)

	now := s.now().UTC()
	if m.ObservedAt.IsZero() {
		m.ObservedAt = now
	}
	stamp := formatStamp(now)
	observedStamp := formatStamp(m.ObservedAt)
	m.CreatedAt = now
	m.UpdatedAt = now
	m.ID = GenerateID(m.Claim, m.Scope, string(m.Authority), sessionID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Memory{}, fmt.Errorf("begin memory tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Read same-scope, non-archived rows. Two consumers:
	//   - spec §4.2 row 1: same normalized claim → idempotent short-circuit
	//     (returns the existing row, no error).
	//   - spec §4.2 row 2: same authority + different claim → dispute marking,
	//     and the new row is itself marked disputed.
	existing, err := loadSameScope(ctx, tx, m.Scope)
	if err != nil {
		return Memory{}, fmt.Errorf("scan existing memories: %w", err)
	}
	for _, row := range existing {
		if normalizeClaim(row.Claim) == normalized {
			memory, loadErr := loadMemoryByID(ctx, tx, row.ID)
			if loadErr != nil {
				return Memory{}, loadErr
			}
			if err := tx.Commit(); err != nil {
				return Memory{}, fmt.Errorf("commit idempotent memory: %w", err)
			}
			committed = true
			return memory, nil
		}
	}

	// Conflict marking: any other same-scope + same-authority row whose claim
	// normalises to something different gets flipped to disputed.
	disputeIDs := make([]string, 0)
	for _, row := range existing {
		if row.ID == m.ID {
			continue
		}
		if row.Authority != m.Authority {
			continue
		}
		if normalizeClaim(row.Claim) == normalized {
			continue
		}
		disputeIDs = append(disputeIDs, row.ID)
	}
	for _, id := range disputeIDs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE memories SET status='disputed', updated_at=? WHERE id=?`,
			stamp, id,
		); err != nil {
			return Memory{}, fmt.Errorf("dispute peer memory %s: %w", id, err)
		}
	}

	// Spec §4.2 row 2 mandates both the existing peer and the incoming row
	// land as `disputed`. Only override the routed Status when the loop above
	// actually flipped a peer; inferred / explicit routing stays intact in
	// every other case.
	if len(disputeIDs) > 0 {
		m.Status = StatusDisputed
	}

	var validFrom, validUntil any
	if m.ValidFrom != nil {
		validFrom = formatStamp(m.ValidFrom.UTC())
	}
	if m.ValidUntil != nil {
		validUntil = formatStamp(m.ValidUntil.UTC())
	}

	// Race-safe insert: ON CONFLICT(id) DO NOTHING collapses the
	// SELECT-then-INSERT race window. Two concurrent writers with the same
	// (claim, scope, authority, sessionID) tuple compute the same id; the
	// first INSERT wins, the second silently no-ops, and the loser's
	// RETURNING clause produces no rows so we fall back to loadMemoryByID
	// and return the durable row. The normalized SELECT above covers the
	// NFD/NFC same-claim case that ON CONFLICT(id) cannot detect on its own.
	var insertedID string
	scanErr := tx.QueryRowContext(ctx, `
INSERT INTO memories(
    id, claim, kind, scope_kind, scope_value, authority, source_type, source_ref,
    observed_at, valid_from, valid_until, status, confidence, evidence_score,
    supersedes, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING RETURNING id`,
		m.ID, m.Claim, m.Kind, m.Scope.Kind, m.Scope.Value,
		string(m.Authority), string(m.Source.Type), m.Source.Ref,
		observedStamp, validFrom, validUntil, string(m.Status),
		m.Confidence, m.EvidenceScore, m.Supersedes, stamp, stamp,
	).Scan(&insertedID)
	if errors.Is(scanErr, sql.ErrNoRows) {
		// Another writer with the same id inserted first. Load the
		// pre-existing row and return it inside this transaction.
		loaded, loadErr := loadMemoryByID(ctx, tx, m.ID)
		if loadErr != nil {
			return Memory{}, loadErr
		}
		if err := tx.Commit(); err != nil {
			return Memory{}, fmt.Errorf("commit race-resolved memory: %w", err)
		}
		committed = true
		return loaded, nil
	}
	if scanErr != nil {
		return Memory{}, fmt.Errorf("insert memory: %w", scanErr)
	}

	inserted, err := loadMemoryByID(ctx, tx, insertedID)
	if err != nil {
		return Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return Memory{}, fmt.Errorf("commit memory insert: %w", err)
	}
	committed = true
	return inserted, nil
}

// existingRow is the trimmed projection we need to drive idempotency and
// conflict detection without hauling every column through memory.
type existingRow struct {
	ID        string
	Claim     string
	Authority Authority
}

// loadSameScope returns every non-archived memory in the given scope.
func loadSameScope(ctx context.Context, tx *sql.Tx, scope Scope) ([]existingRow, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, claim, authority FROM memories WHERE scope_kind = ? AND scope_value = ? AND status != 'archived'`,
		scope.Kind, scope.Value,
	)
	if err != nil {
		return nil, fmt.Errorf("scan scope memories: %w", err)
	}
	defer rows.Close()
	var result []existingRow
	for rows.Next() {
		var r existingRow
		var authority string
		if err := rows.Scan(&r.ID, &r.Claim, &authority); err != nil {
			return nil, fmt.Errorf("scan memory row: %w", err)
		}
		r.Authority = Authority(authority)
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scope memories: %w", err)
	}
	return result, nil
}

// loadMemoryByID reads the full memory row back from the database so the
// caller observes persistence roundtrip (timestamps stamped by SQLite, id
// echoed, etc).
func loadMemoryByID(ctx context.Context, tx *sql.Tx, id string) (Memory, error) {
	row := tx.QueryRowContext(ctx, memoryColumnsSelect+` FROM memories WHERE id = ?`, id)
	m, err := scanMemoryFields(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, fmt.Errorf("memory %s disappeared mid-transaction", id)
	}
	if err != nil {
		return Memory{}, fmt.Errorf("load memory: %w", err)
	}
	return m, nil
}

// memoryColumnsSelect is the canonical column projection shared by every
// read-side query. Keeping it in one place ensures List / Get / Why / future
// loaders all see the same shape scanMemoryFields expects.
const memoryColumnsSelect = `SELECT id, claim, kind, scope_kind, scope_value, authority, source_type, source_ref,
       observed_at, valid_from, valid_until, status, confidence, evidence_score,
       supersedes, created_at, updated_at`

// rowScanner abstracts over *sql.Row and *sql.Rows so a single helper can
// parse the memories table projection regardless of whether the caller holds
// a single-row result (Get, loadMemoryByID) or is iterating rows (List, Why's
// conflict query).
type rowScanner interface {
	Scan(dest ...any) error
}

// scanMemoryFields reads one row produced by memoryColumnsSelect and
// decodes it back into the Memory value type, including the RFC3339Nano
// timestamp columns that the Store layer persists as strings.
func scanMemoryFields(scanner rowScanner) (Memory, error) {
	var (
		m          Memory
		authority  string
		scopeKind  string
		scopeValue sql.NullString
		sourceType string
		status     string
		validFrom  sql.NullString
		validUntil sql.NullString
		supersedes sql.NullString
		observedAt string
		createdAt  string
		updatedAt  string
	)
	if err := scanner.Scan(
		&m.ID, &m.Claim, &m.Kind, &scopeKind, &scopeValue, &authority, &sourceType, &m.Source.Ref,
		&observedAt, &validFrom, &validUntil, &status, &m.Confidence, &m.EvidenceScore,
		&supersedes, &createdAt, &updatedAt,
	); err != nil {
		return Memory{}, err
	}
	m.Scope = Scope{Kind: scopeKind, Value: scopeValue.String}
	m.Authority = Authority(authority)
	m.Source.Type = SourceType(sourceType)
	m.Status = Status(status)
	if supersedes.Valid {
		m.Supersedes = supersedes.String
	}
	if t, err := time.Parse(time.RFC3339Nano, observedAt); err == nil {
		m.ObservedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		m.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		m.UpdatedAt = t
	}
	if validFrom.Valid {
		if t, err := time.Parse(time.RFC3339Nano, validFrom.String); err == nil {
			m.ValidFrom = &t
		}
	}
	if validUntil.Valid {
		if t, err := time.Parse(time.RFC3339Nano, validUntil.String); err == nil {
			m.ValidUntil = &t
		}
	}
	return m, nil
}

// List returns memories matching the dynamic WHERE clause built from q. Empty
// filter fields mean "no constraint on that column"; an empty q matches every
// row. Ordering is evidence_score DESC, observed_at DESC per spec §6.1 Tier 1
// task-topic catalogue. Limit defaults to 20 and is clamped at 200 per spec
// §5; q.Limit < 0 or q.Limit > 200 returns ErrInvalidQuery.
func (s *Store) List(ctx context.Context, q ListQuery) ([]Memory, error) {
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
	if q.ScopeKind != "" {
		clauses = append(clauses, "scope_kind = ?")
		args = append(args, q.ScopeKind)
	}
	if q.ScopeValue != "" {
		clauses = append(clauses, "scope_value = ?")
		args = append(args, q.ScopeValue)
	}
	if q.Authority != "" {
		clauses = append(clauses, "authority = ?")
		args = append(args, q.Authority)
	}
	if q.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, q.Status)
	}

	query := memoryColumnsSelect + " FROM memories"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY evidence_score DESC, observed_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()
	result := make([]Memory, 0, limit)
	for rows.Next() {
		m, err := scanMemoryFields(rows)
		if err != nil {
			return nil, fmt.Errorf("scan memory row: %w", err)
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory rows: %w", err)
	}
	return result, nil
}

// Get returns the single memory row for id, or ErrMemoryNotFound (wrapped
// with the id) if no such row exists. It is a read-only lookup that does
// not open a write transaction.
func (s *Store) Get(ctx context.Context, id string) (Memory, error) {
	row := s.db.QueryRowContext(ctx, memoryColumnsSelect+` FROM memories WHERE id = ?`, id)
	m, err := scanMemoryFields(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, fmt.Errorf("%w: %s", ErrMemoryNotFound, id)
	}
	if err != nil {
		return Memory{}, fmt.Errorf("load memory %s: %w", id, err)
	}
	return m, nil
}

// Why returns the audit surface for `mengdie memory why <id>` (spec §5) and
// the Trust Set's why_completeness metric (spec §7). The report contains all
// six sections the spec lists — source, observed_at, scope (carried inside
// Memory), evidence, conflicts, recent_usage — assembled from four read-only
// queries (no write transaction). Evidence is ordered newest-first; usage is
// capped at the five most-recent recalls; conflicts share the target's
// scope_kind + scope_value and are currently filtered to status='disputed'
// peers (cross-authority conflict peers land in Task 5).
func (s *Store) Why(ctx context.Context, id string) (WhyReport, error) {
	mem, err := s.Get(ctx, id)
	if err != nil {
		return WhyReport{}, err
	}

	// Pre-allocate the slice fields so the CLI formatter can range over them
	// unconditionally without nil checks; matches the Trust Set audit metric.
	report := WhyReport{
		Memory:      mem,
		Source:      mem.Source,
		Evidence:    []Evidence{},
		Conflicts:   []Memory{},
		RecentUsage: []UsageRecord{},
	}

	// Evidence: every corroborating signal, newest-first.
	evRows, err := s.db.QueryContext(ctx, `
SELECT id, memory_id, kind, source_ref, weight, created_at
FROM memory_evidence
WHERE memory_id = ?
ORDER BY created_at DESC`, id)
	if err != nil {
		return WhyReport{}, fmt.Errorf("list evidence: %w", err)
	}
	defer evRows.Close()
	for evRows.Next() {
		var (
			ev        Evidence
			createdAt string
		)
		if err := evRows.Scan(&ev.ID, &ev.MemoryID, &ev.Kind, &ev.SourceRef, &ev.Weight, &createdAt); err != nil {
			return WhyReport{}, fmt.Errorf("scan evidence: %w", err)
		}
		if t, parseErr := time.Parse(time.RFC3339Nano, createdAt); parseErr == nil {
			ev.CreatedAt = t
		}
		report.Evidence = append(report.Evidence, ev)
	}
	if err := evRows.Err(); err != nil {
		return WhyReport{}, fmt.Errorf("iterate evidence: %w", err)
	}

	// Recent usage: capped at the most-recent five recalls per spec §5.
	usageRows, err := s.db.QueryContext(ctx, `
SELECT memory_id, session_id, recalled_at, outcome
FROM memory_usage
WHERE memory_id = ?
ORDER BY recalled_at DESC
LIMIT 5`, id)
	if err != nil {
		return WhyReport{}, fmt.Errorf("list usage: %w", err)
	}
	defer usageRows.Close()
	for usageRows.Next() {
		var (
			rec        UsageRecord
			recalledAt string
			outcome    sql.NullString
		)
		if err := usageRows.Scan(&rec.MemoryID, &rec.SessionID, &recalledAt, &outcome); err != nil {
			return WhyReport{}, fmt.Errorf("scan usage: %w", err)
		}
		if t, parseErr := time.Parse(time.RFC3339Nano, recalledAt); parseErr == nil {
			rec.RecalledAt = t
		}
		if outcome.Valid {
			rec.Outcome = outcome.String
		}
		report.RecentUsage = append(report.RecentUsage, rec)
	}
	if err := usageRows.Err(); err != nil {
		return WhyReport{}, fmt.Errorf("iterate usage: %w", err)
	}

	// Conflicts: any other same-scope row currently marked disputed. The
	// cross-authority expansion of spec §4.2 row 3 is intentionally deferred
	// to Task 5; until then the query is a same-scope + status=disputed sweep.
	conflictRows, err := s.db.QueryContext(ctx, memoryColumnsSelect+`
FROM memories
WHERE scope_kind = ? AND scope_value = ? AND id != ? AND status = 'disputed'
ORDER BY observed_at DESC`, mem.Scope.Kind, mem.Scope.Value, mem.ID)
	if err != nil {
		return WhyReport{}, fmt.Errorf("list conflicts: %w", err)
	}
	defer conflictRows.Close()
	for conflictRows.Next() {
		peer, err := scanMemoryFields(conflictRows)
		if err != nil {
			return WhyReport{}, fmt.Errorf("scan conflict: %w", err)
		}
		report.Conflicts = append(report.Conflicts, peer)
	}
	if err := conflictRows.Err(); err != nil {
		return WhyReport{}, fmt.Errorf("iterate conflicts: %w", err)
	}

	return report, nil
}

// normalizeClaim applies the spec §4.2 equality rule: case-insensitive
// after decomposing to NFD and re-composing to NFC so equivalent composed
// and decomposed forms collide in memory-equality checks.
func normalizeClaim(claim string) string {
	lower := strings.ToLower(claim)
	return norm.NFC.String(norm.NFD.String(lower))
}

// sessionIDFromSource extracts the session identifier from a Source.Ref when
// it follows the "<session>:<seq>:<speaker>" convention used for user/agent
// messages. Falls back to a stable placeholder so the generated id still has
// a deterministic fourth input.
func sessionIDFromSource(ref string) string {
	if ref == "" {
		return "session-default"
	}
	if i := strings.IndexByte(ref, ':'); i > 0 {
		return ref[:i]
	}
	return ref
}

// formatStamp mirrors session.formatTime: RFC3339Nano in UTC. We keep it
// inline rather than exporting FormatTime from the session package because
// the session side is explicit that formatTime is an implementation detail
// and the one-line conversion has no surprise behavior worth a helper.
func formatStamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
