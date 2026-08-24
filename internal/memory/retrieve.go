// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package memory 的 Retriever 层实现规范 §6.1 的三级召回：
// Tier 1 catalogue（常驻能力说明）、Tier 2 task topics（任务级主题目录）、
// Tier 3 atomic recall（原子记忆正文）。Tier 1 与 Tier 2 共享同一组
// active-set 过滤条件（status='active' AND valid_until 未过期）与排序
// 策略（evidence_score DESC, observed_at DESC），但 Tier 1 返回 60 字符
// 截断的精简条目，Tier 2 返回完整 Memory 以便 Agent / CLI 直接渲染
// kind、authority、source 等字段。Tier 3 在前两层之上叠加 FTS5 BM25
// proxy + 评分公式，并按命中数量写入 memory_usage 行供 §5 / §7 审计。
package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Recall limits per spec §6.1 and the existing Store.List contract.
//
//   - Tier 1 mirrors Store.List (default 20, cap 200) so audit lists and
//     catalogue reads stay consistent.
//   - Tier 3 defaults to 5 (the value the plan's "memory recall" tool uses)
//     and caps at 50 as a defensive bound against runaway topK values from
//     agents that pass user-controlled input.
const (
	tier1DefaultLimit  = 20
	tier1MaxLimit      = 200
	tier2DefaultLimit  = 20
	tier3DefaultTopK   = 5
	tier3MaxTopK       = 50
	tier3FetchOverhead = 3 // over-fetch by 3x so post-filter shrinkage can't drop below topK

	// catalogueClaimCap is the spec §6.1 Tier 1 / §5 list output rule:
	// claim 渲染时只保留前 60 字符（每条约 80 字符总宽）。
	catalogueClaimCap = 60
)

// authorityWeight maps each Authority to its scoring weight per spec §6.1
// row 1: explicit=1.0, verified=0.8, repository=0.6, inferred=0.3. Higher
// authority biases the score up so the recall order pushes explicit
// verified knowledge to the top.
var authorityWeight = map[Authority]float64{
	AuthorityExplicit:   1.0,
	AuthorityVerified:   0.8,
	AuthorityRepository: 0.6,
	AuthorityInferred:   0.3,
}

// Conflict penalties per spec §6.1 row 5: disputed memories absorb the
// larger 0.5 deduction (both sides are flagged), stale memories take the
// smaller 0.3 (they still carry historical weight but should sink).
const (
	conflictPenaltyDisputed = 0.5
	conflictPenaltyStale    = 0.3
)

// taskScopeMatchBonus is the +0.5 bonus per spec §6.1 row 4, applied when
// the memory's scope_kind='project' AND scope_value equals the caller's
// target scope value. Project-scope peers rank above non-matching memories
// (e.g. branch-scope or task-scope) at equal bm25.
const taskScopeMatchBonus = 0.5

// defaultRecallSessionID is the session id stamped on every memory_usage
// row produced by the Retriever. The brief pins NewRetriever(store *Store)
// so there is no parameter through which a real session id can flow; Task 7
// (Agent integration) is expected to wire the real session via a follow-up
// constructor or context key. Until then this constant keeps RecordUsage
// contract-compliant (non-empty session_id) so the Why audit / Trust Set
// runner can still observe per-recall usage rows.
const defaultRecallSessionID = "retriever-default"

// Retriever provides the 3-level recall surface per spec §6.1: Tier 1 / 2
// catalogue reads and Tier 3 FTS5-driven atomic recall. It borrows the
// session-owned SQLite connection from Store and does not open its own
// transactions; the only write it issues is RecordUsage per Tier 3 hit.
type Retriever struct {
	store *Store
}

// NewRetriever wraps store so callers can run the 3-level recall pipeline
// against the same SQLite the rest of the memory package reads from.
func NewRetriever(store *Store) *Retriever {
	return &Retriever{store: store}
}

// CatalogueEntry is the trimmed Tier 1 row projection: id is the durable
// memory id, Claim is truncated to 60 chars per spec §6.1 / §5 list
// output, Authority is the source authority classification (explicit /
// verified / repository / inferred) so the Agent renderer can stamp it on
// every catalogue bullet without a follow-up Get, and EvidenceScore is the
// raw recomputed value (no rounding) so the downstream UI can format it
// consistently.
type CatalogueEntry struct {
	ID            string
	Claim         string
	EvidenceScore float64
	Authority     Authority
}

// RecallHit embeds the full Memory plus a Score field carrying the spec
// §6.1 computed ranking. Callers iterate hits in score-descending order;
// the embedded Memory lets downstream code reach ID / Claim / Authority /
// Scope / Source without a follow-up Get.
type RecallHit struct {
	Memory
	Score float64
}

// Tier1Catalogue returns the active memory catalogue for scope, ordered by
// evidence_score DESC, observed_at DESC. Each Claim is truncated to 60
// chars. Filter: status='active' AND (valid_until IS NULL OR valid_until >
// now()). limit defaults to 20 and is capped at 200 (matches Store.List's
// [0, 200] contract from spec §5).
//
// Ancestor-scope expansion (project < branch < task) is a future task per
// the brief; v0.1 performs exact scope_kind + scope_value match.
func (r *Retriever) Tier1Catalogue(ctx context.Context, scope Scope, limit int) ([]CatalogueEntry, error) {
	if err := scope.Valid(); err != nil {
		return nil, fmt.Errorf("tier1 catalogue: %w", err)
	}
	if limit <= 0 {
		limit = tier1DefaultLimit
	}
	if limit > tier1MaxLimit {
		limit = tier1MaxLimit
	}
	now := formatStamp(r.store.now().UTC())

	rows, err := r.store.db.QueryContext(ctx, `
SELECT id, substr(claim, 1, ?), evidence_score, authority
FROM memories
WHERE scope_kind = ? AND scope_value = ?
  AND status = 'active'
  AND (valid_until IS NULL OR valid_until > ?)
ORDER BY evidence_score DESC, observed_at DESC
LIMIT ?`,
		catalogueClaimCap, scope.Kind, scope.Value, now, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("tier1 catalogue query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]CatalogueEntry, 0, limit)
	for rows.Next() {
		var (
			e         CatalogueEntry
			authority string
		)
		if err := rows.Scan(&e.ID, &e.Claim, &e.EvidenceScore, &authority); err != nil {
			return nil, fmt.Errorf("scan tier1 entry: %w", err)
		}
		e.Authority = Authority(authority)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tier1 entries: %w", err)
	}
	return out, nil
}

// Tier2TaskTopics is the spec §6.1 Tier 2 surface: the same scope-filtered
// active-set as Tier 1, but each entry is the full Memory so the Agent /
// CLI can render kind, authority, source, etc. without a follow-up Get.
// Default limit is 20 per spec §6.1.
//
// Tier 1 and Tier 2 share the underlying query shape (active filter +
// scope + ordering); the only difference is the projection. We keep the
// SQL duplicated so each method can evolve independently — e.g. Tier 2
// may later include task-scope peers via the ancestor-scope expansion
// planned for a later slice.
func (r *Retriever) Tier2TaskTopics(ctx context.Context, scope Scope) ([]Memory, error) {
	if err := scope.Valid(); err != nil {
		return nil, fmt.Errorf("tier2 task topics: %w", err)
	}
	now := formatStamp(r.store.now().UTC())

	rows, err := r.store.db.QueryContext(ctx, memoryColumnsSelect+`
FROM memories
WHERE scope_kind = ? AND scope_value = ?
  AND status = 'active'
  AND (valid_until IS NULL OR valid_until > ?)
ORDER BY evidence_score DESC, observed_at DESC
LIMIT ?`,
		scope.Kind, scope.Value, now, tier2DefaultLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("tier2 task topics query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Memory, 0, tier2DefaultLimit)
	for rows.Next() {
		m, err := scanMemoryFields(rows)
		if err != nil {
			return nil, fmt.Errorf("scan tier2 memory: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tier2 memories: %w", err)
	}
	return out, nil
}

// Tier3AtomicRecall runs the spec §6.1 Tier 3 pipeline:
//  1. FTS5 search with over-fetch (3x) so post-filter shrinkage can't drop
//     us below topK.
//  2. Load Memory rows for each candidate via the rowid join.
//  3. Apply active-set filter (status='active' AND valid_until valid) —
//     done in SQL alongside the FTS5 match so the SQL engine prunes early.
//  4. Compute score per spec §6.1 formula:
//     score = -bm25 + authority_weight + evidence_score + scope_match*0.5 - conflict_penalty
//     bm25 is proxied by -FTS5_rank (FTS5 rank is negative-better).
//  5. Sort by score desc and truncate to top topK.
//  6. Insert memory_usage rows (idempotent via composite PK).
//
// topK defaults to 5 and is capped at 50 (defensive). Scope matching is
// exact scope_kind + scope_value match; ancestor-scope expansion is a
// future task.
func (r *Retriever) Tier3AtomicRecall(ctx context.Context, query string, topK int, scope Scope) ([]RecallHit, error) {
	if err := scope.Valid(); err != nil {
		return nil, fmt.Errorf("tier3 atomic recall: %w", err)
	}
	if topK <= 0 {
		topK = tier3DefaultTopK
	}
	if topK > tier3MaxTopK {
		topK = tier3MaxTopK
	}

	ftsQuery := buildFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	now := formatStamp(r.store.now().UTC())
	fetchLimit := topK * tier3FetchOverhead

	// FTS5 search joined back to memories on rowid so we can apply the
	// active-set filter, scope filter, and re-load the full Memory in one
	// round trip. The fts.rank column is the engine's bm25-with-defaults
	// proxy: it is negative and lower-is-better, so we negate it before
	// folding into the spec formula (which is written as `score = -bm25 + ...`,
	// i.e. higher-is-better).
	//
	// Note on the join: memories has an INTEGER PRIMARY KEY named `rowid`,
	// which the migration also passes to FTS5 via content_rowid='rowid'. The
	// join is therefore exact; FTS5's rowid === memories.rowid for every
	// indexed row.
	rows, err := r.store.db.QueryContext(ctx, `
SELECT m.id, m.claim, m.kind, m.scope_kind, m.scope_value, m.authority, m.source_type, m.source_ref,
       m.observed_at, m.valid_from, m.valid_until, m.status, m.confidence, m.evidence_score,
       m.supersedes, m.created_at, m.updated_at,
       fts.rank
FROM memories_fts AS fts
JOIN memories AS m ON m.rowid = fts.rowid
WHERE fts.claim MATCH ?
  AND m.scope_kind = ? AND m.scope_value = ?
  AND m.status = 'active'
  AND (m.valid_until IS NULL OR m.valid_until > ?)
ORDER BY fts.rank
LIMIT ?`,
		ftsQuery, scope.Kind, scope.Value, now, fetchLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("tier3 fts search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Memory scanning is duplicated from store.go's scanMemoryFields because
	// this query selects one extra column (fts.rank) at the end; inlining
	// the scan keeps the column ordering stable without disturbing the
	// shared helper.
	var candidates []struct {
		mem  Memory
		rank float64
	}
	for rows.Next() {
		m, rank, err := scanTier3Candidate(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, struct {
			mem  Memory
			rank float64
		}{mem: m, rank: rank})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tier3 candidates: %w", err)
	}

	scored := make([]RecallHit, 0, len(candidates))
	for _, c := range candidates {
		// `-rank` converts FTS5's negative-better rank into a positive-better
		// bm25 proxy the spec formula expects.
		s := scoreRecall(c.mem, -c.rank, scope)
		scored = append(scored, RecallHit{Memory: c.mem, Score: s})
	}
	sortRecallHitsDesc(scored)

	if len(scored) > topK {
		scored = scored[:topK]
	}

	// RecordUsage per returned hit per spec §6.1 ("召回同时调用
	// Store.RecordUsage(memory_id, session_id, outcome=unknown)"). The
	// composite PK on memory_usage plus the OR IGNORE in Store.RecordUsage
	// absorb duplicates inside the same millisecond; the failure mode of
	// a non-unique insert cannot surface here.
	for _, hit := range scored {
		if err := r.store.RecordUsage(ctx, UsageRecord{
			MemoryID:  hit.ID,
			SessionID: defaultRecallSessionID,
			Outcome:   "unknown",
		}); err != nil {
			return nil, fmt.Errorf("record usage for %s: %w", hit.ID, err)
		}
	}

	return scored, nil
}

// scanTier3Candidate reads one row from the Tier 3 candidate query: the
// canonical 17 memory columns followed by the trailing fts.rank. We inline
// the timestamp parsing from scanMemoryFields rather than teach the shared
// helper about a varying tail because that would force every other caller
// to provide a dummy value.
func scanTier3Candidate(rows *sql.Rows) (Memory, float64, error) {
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
		rank       float64
	)
	if err := rows.Scan(
		&m.ID, &m.Claim, &m.Kind, &scopeKind, &scopeValue, &authority, &sourceType, &m.Source.Ref,
		&observedAt, &validFrom, &validUntil, &status, &m.Confidence, &m.EvidenceScore,
		&supersedes, &createdAt, &updatedAt, &rank,
	); err != nil {
		return Memory{}, 0, fmt.Errorf("scan tier3 candidate: %w", err)
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
	return m, rank, nil
}

// scoreRecall applies the spec §6.1 scoring formula to one candidate:
//
//	score = bm25_proxy + authority_weight + evidence_score + scope_match*0.5 - conflict_penalty
//
// bm25Proxy is the pre-converted positive-better bm25 score (= -FTS5_rank).
// scope is the caller's target scope (used only for the project-scope match
// bonus). The match condition is scope_kind='project' AND the memory's
// scope_value equals the caller's scope_value.
func scoreRecall(m Memory, bm25Proxy float64, scope Scope) float64 {
	w := authorityWeight[m.Authority]
	match := 0.0
	if m.Scope.Kind == "project" && scope.Kind == "project" && m.Scope.Value == scope.Value {
		match = taskScopeMatchBonus
	}
	penalty := 0.0
	switch m.Status {
	case StatusDisputed:
		penalty = conflictPenaltyDisputed
	case StatusStale:
		penalty = conflictPenaltyStale
	}
	return bm25Proxy + w + m.EvidenceScore + match - penalty
}

// sortRecallHitsDesc sorts hits in-place by Score descending. Insertion sort
// keeps the call site allocation-free; the slice is bounded by tier3MaxTopK
// (≤ 50) so the O(n²) cost is irrelevant.
func sortRecallHitsDesc(hits []RecallHit) {
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j-1].Score < hits[j].Score; j-- {
			hits[j-1], hits[j] = hits[j], hits[j-1]
		}
	}
}

// buildFTSQuery converts a free-text query into an FTS5 MATCH expression.
// Each whitespace-separated term is double-quoted (with internal quotes
// doubled per FTS5 syntax) and joined with spaces, which FTS5 treats as an
// implicit AND. Empty / whitespace-only queries return "" so the caller
// can short-circuit without firing an FTS5 syntax error.
//
// We intentionally avoid letting FTS5 operators (AND / OR / NOT / NEAR / *)
// through: a CLI / Agent user typing raw text never accidentally triggers a
// parse error or an unexpected expansion. The cost is that exact-phrase
// queries are not supported in v0.1 — the Tier 3 recall surface treats
// every whitespace gap as an implicit AND of tokens, which matches the
// recall semantics the brief asks for.
func buildFTSQuery(query string) string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
	}
	return strings.Join(parts, " ")
}
