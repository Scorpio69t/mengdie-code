// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package proposal

import (
	"context"
	"fmt"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/memory/extractor"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// Pipeline runs the 5-stage Reflect / Consolidate pipeline described in
// ARCHITECTURE §9.3: Scan → Extract → Verify → Reflect → Propose.
//
// v0.1 only supports manual trigger (no daemon / idle / cron). The
// Reflect stage dispatches to 5 rule-based patterns in patterns.go —
// Task 3 implements the detection logic, this file only wires the
// orchestration. The Propose stage writes to the proposals table;
// downstream apply (AGENTS.md / Skill / memory) is a v0.2 follow-up
// gated on the acceptance-rate threshold once the review queue has
// collected enough signal.
type Pipeline struct {
	sessionStore  *session.SQLiteStore
	memoryStore   *memory.Store
	proposalStore *Store
	now           func() time.Time
}

// New returns a Pipeline over the given dependency graph. All four
// arguments are required; nil values are accepted only where Go's own
// conventions allow (now defaults to time.Now) — the pipeline keeps no
// global state.
func New(ss *session.SQLiteStore, ms *memory.Store, ps *Store, now func() time.Time) *Pipeline {
	return &Pipeline{sessionStore: ss, memoryStore: ms, proposalStore: ps, now: now}
}

// ScannedSession is one session's worth of durable facts pulled by Stage
// 1. Events is the narrow session.EventRow projection (not the full
// events.Envelope) because the pipeline deliberately stays decoupled
// from the wire-format JSON payload — the row projection is what
// session.SQLiteStore.Events already returns, and downstream rules
// (extractor.Rules, the Stage 4 patterns) consume that same shape so
// adding another decoder would only repeat work.
//
// FirstRunAt / LastRunAt bound the timeline the Stage 4 patterns
// inspect; both come from eventTimeBounds so the values line up across
// the same field set.
//
// Memories is the per-session memory slice extracted by Stage 2 (the
// M3 Slice 02 hybrid extractor). v0.1 populates it during extract so
// the Stage 4 patterns read pre-fetched rows without re-querying and
// without taking a *memory.Store parameter — keeping the proposal
// package's only dependency on internal/memory the value-type
// reference on this field. Future slices may re-shape population (e.g.
// Trust Set runner pre-filter) without changing this field's shape.
type ScannedSession struct {
	SessionID  string
	Events     []session.EventRow
	FirstRunAt time.Time
	LastRunAt  time.Time
	Memories   []memory.Memory
}

// ReflectOptions are the runtime knobs for one Reflect invocation.
//
//   - Since bounds Stage 1's session list — sessions whose last
//     event.created_at predates Since are filtered out. Zero value
//     defaults to now - defaultSinceWindow (7d).
//   - SessionIDs is an explicit allow-list. Non-empty list bypasses the
//     `SELECT DISTINCT session_id ORDER BY MAX(created_at)` scan and
//     reads events only for the listed sessions; the cap still applies.
//   - MaxSessions caps how many distinct sessions Stage 1 will inspect
//     per call. Zero value defaults to defaultMaxSessions (5).
type ReflectOptions struct {
	Since       time.Time
	SessionIDs  []string
	MaxSessions int
}

const (
	// defaultSinceWindow caps a single Reflect invocation's recency
	// horizon. 7d matches the spec §9.3 "weekly review" cadence and keeps
	// one Reflect pass bounded so a backlogged session table does not
	// block the CLI.
	defaultSinceWindow = 7 * 24 * time.Hour
	// defaultMaxSessions caps the per-call batch size so a slow Stage 2
	// or Stage 4 never drags an interactive CLI invocation across
	// thousands of sessions. 5 mirrors the v0.1 weekly-trim ceremony.
	defaultMaxSessions = 5
	// eventsPerSessionMax caps how many EventRow records Stage 1 reads
	// per scanned session. 1000 mirrors session.SQLiteStore's existing
	// maximumLoadLimit so the read is bounded by the same physical
	// constraint the storage layer already enforces.
	eventsPerSessionMax = 1000
)

// Reflect runs the 5-stage pipeline end-to-end and returns the proposals
// it inserted (matching the same rows the proposalStore now holds).
//
// Errors are wrapped with the failing stage name (scan / extract /
// propose) so the CLI / future daemon can surface the precise failure
// without unwrapping. The reflect stage itself never returns an error —
// it is deterministic and pattern-based; a future Task 5 may add LLM
// calls here and start bubbling errors up.
func (p *Pipeline) Reflect(ctx context.Context, opts ReflectOptions) ([]Proposal, error) {
	if opts.MaxSessions <= 0 {
		opts.MaxSessions = defaultMaxSessions
	}
	if opts.Since.IsZero() {
		opts.Since = p.now().Add(-defaultSinceWindow)
	}

	// Stage 1: Scan.
	sessions, err := p.scan(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	// Stage 2: Extract. Reuses M3 Slice 02 hybrid extractor; v0.1 only
	// runs the Rules half (NewHybrid(rules, nil)) so the Reflect Worker
	// does not need a Provider dependency for now. Task 5 may thread
	// the LLM half in via the same NewHybrid call.
	candidates, err := p.extract(ctx, sessions)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}

	// Stage 3: Verify. v0.1 simplified: candidates pass through with
	// their source_type / confidence unchanged. A future Trust-Set-
	// aware slice may add cross-source corroboration here.
	verified := p.verify(candidates)

	// Stage 4: Reflect. Dispatches the 5 pattern detectors in
	// patterns.go. Detectors that need memory.Store or event-shape
	// inspection will be wired up by Task 3.
	proposals := p.reflect(ctx, verified, sessions)

	// Stage 5: Propose. INSERT every proposal into the
	// reflection_proposals table so the CLI review queue can list them
	// (status='proposed' is the default filter).
	if err := p.propose(ctx, proposals); err != nil {
		return nil, fmt.Errorf("propose: %w", err)
	}

	return proposals, nil
}

// scan reads the most-recent N sessions' events (Stage 1).
//
// When opts.SessionIDs is non-empty the function skips the global scan
// and reads events only for those sessions — this is the daemon /
// resume path's "Reflect just this session" entry point. Otherwise
// scan queries events for distinct session_ids observed within the
// recency window, ordered by most-recent event.created_at, and capped
// at opts.MaxSessions.
func (p *Pipeline) scan(ctx context.Context, opts ReflectOptions) ([]ScannedSession, error) {
	sessionIDs := opts.SessionIDs
	if len(sessionIDs) == 0 {
		rows, err := p.sessionStore.DB().QueryContext(ctx, `
SELECT session_id, MAX(created_at) AS last_at
FROM events
WHERE created_at >= ?
GROUP BY session_id
ORDER BY last_at DESC
LIMIT ?`, formatStamp(opts.Since.UTC()), opts.MaxSessions,
		)
		if err != nil {
			return nil, fmt.Errorf("list sessions: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			var lastAt string
			if err := rows.Scan(&id, &lastAt); err != nil {
				return nil, fmt.Errorf("scan session id: %w", err)
			}
			sessionIDs = append(sessionIDs, id)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate session ids: %w", err)
		}
	}

	sessions := make([]ScannedSession, 0, len(sessionIDs))
	for _, sid := range sessionIDs {
		rows, err := p.sessionStore.Events(ctx, sid, eventsPerSessionMax)
		if err != nil {
			return nil, fmt.Errorf("events %s: %w", sid, err)
		}
		if len(rows) == 0 {
			continue
		}
		first, last := eventTimeBounds(rows)
		sessions = append(sessions, ScannedSession{
			SessionID:  sid,
			Events:     rows,
			FirstRunAt: first,
			LastRunAt:  last,
		})
	}
	return sessions, nil
}

// extract runs the M3 Slice 02 hybrid extractor per session (Stage 2).
//
// A fresh NewSQLiteReader is built per session instead of being shared —
// the underlying *session.SQLiteStore is connection-pooled (single
// writer) so this costs nothing and keeps each Reflect invocation
// independent of the reader's binding scope. NewHybrid(rules, nil)
// runs the Rules-only path so no LLM Provider is required in v0.1.
//
// Each session's per-row extraction slice is also written back to
// sessions[i].Memories so the Stage 4 patterns read the same rows
// without re-running the extractor (and without taking a *memory.Store
// parameter in their signatures).
func (p *Pipeline) extract(ctx context.Context, sessions []ScannedSession) ([]memory.Memory, error) {
	var candidates []memory.Memory
	for i, s := range sessions {
		reader := extractor.NewSQLiteReader(p.sessionStore)
		ext := extractor.NewHybrid(extractor.NewRules(reader), nil) // v0.1 rules-only
		c, err := ext.Extract(ctx, s.SessionID)
		if err != nil {
			return nil, fmt.Errorf("extract %s: %w", s.SessionID, err)
		}
		candidates = append(candidates, c...)
		sessions[i].Memories = c
	}
	return candidates, nil
}

// verify is the pass-through Stage 3. v0.1 does not yet run a second
// corroboration pass — cross-source evidence checks (Trust Set
// integration) and conflict resolution against existing memory rows
// land in a later slice once the review queue has signal.
func (p *Pipeline) verify(candidates []memory.Memory) []memory.Memory {
	return candidates
}

// reflect dispatches the 5 Stage 4 pattern detectors in
// patterns.go and concatenates their proposals. detectors that need
// cross-session context (DetectCrossSessionPattern, DetectObsoleteClaim)
// receive the same sessions list every other detector sees so the
// ownership contract is uniform — Task 3 may extend it with extra
// surfaces (memory.Store.List) without having to change the dispatcher.
func (p *Pipeline) reflect(ctx context.Context, candidates []memory.Memory, sessions []ScannedSession) []Proposal {
	var proposals []Proposal
	proposals = append(proposals, DetectRepeatedCorrection(sessions)...)
	proposals = append(proposals, DetectRepeatedToolPreference(sessions)...)
	proposals = append(proposals, DetectForgottenTest(sessions)...)
	proposals = append(proposals, DetectCrossSessionPattern(sessions)...)
	proposals = append(proposals, DetectObsoleteClaim(sessions)...)
	return proposals
}

// propose inserts every proposal into the reflection_proposals table
// (Stage 5). Each insertion is independent — a single bad proposal
// (e.g. validation failure) does not abort the batch, but the caller
// still surfaces the error so the CLI can flag the proposal's title.
func (p *Pipeline) propose(ctx context.Context, proposals []Proposal) error {
	for _, prop := range proposals {
		if _, err := p.proposalStore.Insert(ctx, prop); err != nil {
			return fmt.Errorf("propose %s: %w", prop.Title, err)
		}
	}
	return nil
}

// eventTimeBounds returns the minimum and maximum Timestamp across
// rows. Zero values are skipped on both ends so an all-zero input
// cleanly returns (zero, zero) instead of (zero, zero-from-zero);
// downstream code should treat a zero pair as "no signal" rather than
// a real window. last is updated unconditionally with the comparison
// order from a time.Time.After call so a single-row slice still
// produces first == last.
func eventTimeBounds(rows []session.EventRow) (first, last time.Time) {
	for _, r := range rows {
		t := r.Timestamp
		if t.IsZero() {
			continue
		}
		if first.IsZero() || t.Before(first) {
			first = t
		}
		if t.After(last) {
			last = t
		}
	}
	return first, last
}
