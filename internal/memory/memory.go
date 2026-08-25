// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package memory defines the trusted-memory value types and identifier
// algorithm that map onto the memories / memory_evidence / memory_usage
// tables created by internal/session/migrations/008_memory.sql.
//
// 本包实现规范 §3（schema 类型）与 §4.1（Authority 写入守门表）规定的 Go
// 层类型；这些类型被后续的 Store（Task 3）、Retriever 与 CLI 子命令复用，
// 不直接持有数据库连接，也不发起任何 SQL 查询。
//
// 时间戳在 Go 层使用 time.Time；Store 层通过 session.formatTime 将其序列化为
// RFC3339Nano UTC 字符串，与现有 session 持久化约定保持一致。
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

// Authority classifies how a memory entered the system. The wire value is the
// string literal enforced by the CHECK constraint on memories.authority.
type Authority string

const (
	// AuthorityExplicit marks a memory that the user said directly (e.g.
	// "/remember" or `mengdie memory remember`). Authoritative as long as
	// the user has not contradicted it.
	AuthorityExplicit Authority = "explicit"
	// AuthorityRepository marks a memory verified against a file in the
	// repository (with line offset captured in source_ref).
	AuthorityRepository Authority = "repository"
	// AuthorityVerified marks a memory backed by a passing test, build, or
	// lint run (source_ref carries the command summary and exit code).
	AuthorityVerified Authority = "verified"
	// AuthorityInferred marks a memory proposed by the model at the end of
	// a task. Requires explicit approval before becoming active.
	AuthorityInferred Authority = "inferred"
)

// AuthorityRank returns the rank integer for an Authority value. Lower is
// more authoritative. Used by cross-authority dispute detection (spec
// §4.2 row 3) and by the fingerprint auto-Approve guard (slice 04 §3.4).
// Unknown values default to math.MaxInt so they never displace known ones.
func AuthorityRank(a Authority) int {
	switch a {
	case AuthorityExplicit:
		return 1
	case AuthorityVerified:
		return 2
	case AuthorityRepository:
		return 3
	case AuthorityInferred:
		return 4
	default:
		return math.MaxInt
	}
}

// Scope identifies the lifetime of a memory. Kind is one of the four values
// pinned by the CHECK constraint on memories.scope_kind; Value is the scope
// key (empty only for kind="user").
type Scope struct {
	Kind  string
	Value string
}

// Valid checks that Scope.Kind is one of the four wire values and that Value
// is non-empty for non-global scopes.
func (s Scope) Valid() error {
	switch s.Kind {
	case "user":
		return nil
	case "project", "branch", "task":
		if strings.TrimSpace(s.Value) == "" {
			return fmt.Errorf("memory scope %q requires a non-empty value", s.Kind)
		}
		return nil
	default:
		return fmt.Errorf("unsupported memory scope_kind %q", s.Kind)
	}
}

// Status tracks where a memory sits in the lifecycle. The wire value is the
// string literal enforced by the CHECK constraint on memories.status.
type Status string

const (
	// StatusProposed is the initial status for authority=inferred memories.
	// They become active only via `memory approve <id>`.
	StatusProposed Status = "proposed"
	// StatusActive is the steady state for explicit/repository/verified and
	// for approved inferred memories.
	StatusActive Status = "active"
	// StatusStale is set when valid_until has elapsed.
	StatusStale Status = "stale"
	// StatusDisputed is set when two memories in overlapping scopes disagree.
	StatusDisputed Status = "disputed"
	// StatusSuperseded is set when another memory explicitly replaces this one.
	StatusSuperseded Status = "superseded"
	// StatusArchived is the soft-deleted terminal state.
	StatusArchived Status = "archived"
)

// SourceType names the channel that produced a memory. The Store layer uses
// string equality with these constants when enforcing Authority routing per
// spec §4.1.
type SourceType string

const (
	SourceTypeUserMessage   SourceType = "user_message"
	SourceTypeAgentMessage  SourceType = "agent_message"
	SourceTypeSessionEvent  SourceType = "session_event"
	SourceTypeFile          SourceType = "file"
	SourceTypeCommandResult SourceType = "command_result"
)

// SourceRef points at the concrete artifact that produced a memory.
type SourceRef struct {
	Type SourceType
	Ref  string
}

// Valid checks that Type is one of the known SourceType constants and Ref is
// non-empty. It does NOT enforce Authority ↔ SourceType pairing; that gate
// lives in the Store layer per spec §4.1.
func (r SourceRef) Valid() error {
	switch r.Type {
	case SourceTypeUserMessage, SourceTypeAgentMessage, SourceTypeSessionEvent,
		SourceTypeFile, SourceTypeCommandResult:
	default:
		return fmt.Errorf("unsupported memory source_type %q", r.Type)
	}
	if strings.TrimSpace(r.Ref) == "" {
		return errors.New("memory source_ref is required")
	}
	return nil
}

// Memory is the in-memory representation of one row in the memories table.
// Persistent timestamps are RFC3339Nano UTC strings; the Store layer
// translates between time.Time and the wire format via session.formatTime.
type Memory struct {
	ID            string
	Claim         string
	Kind          string
	Scope         Scope
	Authority     Authority
	Source        SourceRef
	ObservedAt    time.Time
	ValidFrom     *time.Time
	ValidUntil    *time.Time
	Status        Status
	Confidence    float64
	EvidenceScore float64
	Supersedes    string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Evidence records a single corroborating signal for a memory. Per spec §3
// the Kind column is free-form at the schema level; the Store layer pins it
// to one of "user_confirmed" / "reobserved" / "task_verified" so the
// evidence_score formula can weight each kind.
type Evidence struct {
	ID        string
	MemoryID  string
	Kind      string
	SourceRef string
	Weight    float64
	CreatedAt time.Time
}

// UsageRecord records one recall of a memory inside a session. Outcome is
// free-form at the schema level; the Retriever/Store layer pins it to one
// of "unknown" / "helpful" / "harmful" / "unused".
type UsageRecord struct {
	MemoryID   string
	SessionID  string
	RecalledAt time.Time
	Outcome    string
}

// GenerateID returns a stable identifier for the (claim, scope, authority,
// sessionID) tuple. Same inputs always produce the same id, so re-saving an
// idempotent memory yields the same id and does not create a duplicate row
// (idempotency per spec §4.2). Different scopes or authorities naturally
// diverge because every input field participates in the hash.
//
// The format is "mem_" + the first 16 bytes (32 hex chars) of
// sha256(scope.Kind || "\x00" || scope.Value || "\x00" || claim ||
// "\x00" || authority || "\x00" || sessionID). The NUL separators prevent
// collisions between inputs like ("ab","cd") and ("a","bcd").
func GenerateID(claim string, scope Scope, authority string, sessionID string) string {
	h := sha256.New()
	h.Write([]byte(scope.Kind))
	h.Write([]byte("\x00"))
	h.Write([]byte(scope.Value))
	h.Write([]byte("\x00"))
	h.Write([]byte(claim))
	h.Write([]byte("\x00"))
	h.Write([]byte(authority))
	h.Write([]byte("\x00"))
	h.Write([]byte(sessionID))
	sum := h.Sum(nil)
	return "mem_" + hex.EncodeToString(sum[:16])
}

// CanonicalizeClaim returns the claim string in a form suitable for
// idempotency / deduplication comparisons per spec §4.2: case-folded via
// strings.ToLower, then round-tripped through NFD→NFC so that composed
// ("é" = U+00E9) and decomposed ("e" + U+0301) sequences collide.
//
// This is the **single source of truth** for claim normalization across
// the memory package. Store.Save uses it for the idempotent-insert SELECT
// (and the conflict-marking loop) and the Hybrid extractor uses it for
// rules-vs-LLM deduplication; both paths therefore see exactly the same
// equality semantics, so a rule-extracted claim and a DB-stored row with
// the same canonical form cannot drift apart.
//
// Order choice: NFD first decomposes to the canonical base form (so a
// precomposed "é" and a decomposed "e+◌́" both yield "e" + combining
// acute), then NFC re-composes for storage / hash stability. NFD-first
// is the safe default for case-folded equality and is the order the
// Store.Save idempotency path already implemented. Leading / trailing
// whitespace is intentionally NOT trimmed: callers should validate
// non-empty claims explicitly (Store.Save rejects empty / whitespace-only
// claims at the validation gate) and the persisted claim is whatever
// the caller passed in.
func CanonicalizeClaim(claim string) string {
	lower := strings.ToLower(claim)
	return norm.NFC.String(norm.NFD.String(lower))
}
