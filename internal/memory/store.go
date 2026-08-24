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
//   - 同 scope + 同 authority + 不同 claim 的现有 memory 在新行写入前先置为
//     disputed，让 `mengdie memory why <id>` 能给出冲突链。
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
)

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
// every public Save* method. It owns its own transaction so the SELECT-then-
// INSERT pair cannot race with another writer in the same scope.
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

	// Idempotency: scan same-scope, non-archived rows and short-circuit on a
	// normalized-claim match. Returning the existing row keeps new wiring in
	// step with old — see spec §4.2 idempotency row.
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

	var validFrom, validUntil any
	if m.ValidFrom != nil {
		validFrom = formatStamp(m.ValidFrom.UTC())
	}
	if m.ValidUntil != nil {
		validUntil = formatStamp(m.ValidUntil.UTC())
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO memories(
    id, claim, kind, scope_kind, scope_value, authority, source_type, source_ref,
    observed_at, valid_from, valid_until, status, confidence, evidence_score,
    supersedes, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Claim, m.Kind, m.Scope.Kind, m.Scope.Value,
		string(m.Authority), string(m.Source.Type), m.Source.Ref,
		observedStamp, validFrom, validUntil, string(m.Status),
		m.Confidence, m.EvidenceScore, m.Supersedes, stamp, stamp,
	); err != nil {
		return Memory{}, fmt.Errorf("insert memory: %w", err)
	}

	inserted, err := loadMemoryByID(ctx, tx, m.ID)
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
	err := tx.QueryRowContext(ctx, `
SELECT id, claim, kind, scope_kind, scope_value, authority, source_type, source_ref,
       observed_at, valid_from, valid_until, status, confidence, evidence_score,
       supersedes, created_at, updated_at
FROM memories WHERE id = ?`, id).Scan(
		&m.ID, &m.Claim, &m.Kind, &scopeKind, &scopeValue, &authority, &sourceType, &m.Source.Ref,
		&observedAt, &validFrom, &validUntil, &status, &m.Confidence, &m.EvidenceScore,
		&supersedes, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, fmt.Errorf("memory %s disappeared mid-transaction", id)
	}
	if err != nil {
		return Memory{}, fmt.Errorf("load memory: %w", err)
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
