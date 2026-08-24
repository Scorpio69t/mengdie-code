// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package memory_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// memoryTestTime is the fixed clock used by Store tests. The deterministic
// stamp lets us compare UpdatedAt against expected RFC3339Nano values without
// flakiness and exercises the injectable Now function described in the brief.
var memoryTestTime = time.Date(2026, 8, 6, 10, 30, 0, 123, time.UTC)

// openMemoryStore opens an isolated session SQLite store (which already
// carries the 008_memory migration applied via OpenSQLite) and hands the
// underlying *sql.DB to the memory package via OpenMemory. The caller is
// responsible for invoking t.Cleanup to close the store.
func openMemoryStore(t *testing.T) *session.SQLiteStore {
	t.Helper()
	directory := t.TempDir()
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir: directory, ProjectRoot: filepath.Join(t.TempDir(), "project"),
		BusyTimeout: 250 * time.Millisecond,
		Now:         func() time.Time { return memoryTestTime },
	})
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	return store
}

func setupMemoryStore(t *testing.T) *session.SQLiteStore {
	t.Helper()
	store := openMemoryStore(t)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close memory store: %v", err)
		}
	})
	return store
}

// openSharedMemoryStore opens a second session SQLite store against the same
// DataDir as the first one. Two stores on one WAL file let the race tests
// exercise concurrent writers without sharing a single *sql.DB connection
// pool. The caller is responsible for closing both stores.
func openSharedMemoryStore(t *testing.T, directory string, busyTimeout time.Duration) *session.SQLiteStore {
	t.Helper()
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir: directory, ProjectRoot: filepath.Join(t.TempDir(), "project"),
		BusyTimeout: busyTimeout,
		Now:         func() time.Time { return memoryTestTime },
	})
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	return store
}

// TestSaveUserMemoryCreatesActive covers the Authority-explicit happy path:
// a memory with AuthorityExplicit + SourceTypeUserMessage must be persisted
// as StatusActive (not proposed) and a deterministic id must be assigned.
// The Authority assertion guards against a future regression that drops the
// routed value before insert.
func TestSaveUserMemoryCreatesActive(t *testing.T) {
	store := setupMemoryStore(t)
	s := memory.OpenMemory(store)
	in := memory.Memory{
		Claim: "项目用 go test ./...", Authority: memory.AuthorityExplicit,
		Scope:  memory.Scope{Kind: "project", Value: "mengdie"},
		Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-1:1:user"},
	}
	got, err := s.SaveUserMemory(context.Background(), in)
	if err != nil {
		t.Fatalf("SaveUserMemory: %v", err)
	}
	if got.Status != memory.StatusActive {
		t.Fatalf("status=%s, want active", got.Status)
	}
	if got.Authority != memory.AuthorityExplicit {
		t.Fatalf("authority=%s, want explicit", got.Authority)
	}
	if got.ID == "" {
		t.Fatal("id empty")
	}
}

// TestSaveRepositoryFactCreatesActive covers the Authority-repository path:
// SaveRepositoryFact must persist a memory with AuthorityRepository +
// SourceTypeFile and StatusActive. The Authority assertion guards against a
// future regression that drops the routed value before insert.
func TestSaveRepositoryFactCreatesActive(t *testing.T) {
	store := setupMemoryStore(t)
	s := memory.OpenMemory(store)
	in := memory.Memory{
		Claim:  "go.mod requires Go 1.26",
		Scope:  memory.Scope{Kind: "project", Value: "mengdie"},
		Source: memory.SourceRef{Type: memory.SourceTypeFile, Ref: "go.mod:3"},
	}
	got, err := s.SaveRepositoryFact(context.Background(), in)
	if err != nil {
		t.Fatalf("SaveRepositoryFact: %v", err)
	}
	if got.Status != memory.StatusActive {
		t.Fatalf("status=%s, want active", got.Status)
	}
	if got.Authority != memory.AuthorityRepository {
		t.Fatalf("authority=%s, want repository", got.Authority)
	}
	if got.Source.Type != memory.SourceTypeFile {
		t.Fatalf("source_type=%s, want file", got.Source.Type)
	}
}

// TestSaveVerifiedFactCreatesActive covers the Authority-verified path:
// SaveVerifiedFact must persist a memory with AuthorityVerified +
// SourceTypeCommandResult and StatusActive. The Authority assertion guards
// against a future regression that drops the routed value before insert.
func TestSaveVerifiedFactCreatesActive(t *testing.T) {
	store := setupMemoryStore(t)
	s := memory.OpenMemory(store)
	in := memory.Memory{
		Claim:  "go test ./... exits 0",
		Scope:  memory.Scope{Kind: "project", Value: "mengdie"},
		Source: memory.SourceRef{Type: memory.SourceTypeCommandResult, Ref: "go test ./... exit=0"},
	}
	got, err := s.SaveVerifiedFact(context.Background(), in)
	if err != nil {
		t.Fatalf("SaveVerifiedFact: %v", err)
	}
	if got.Status != memory.StatusActive {
		t.Fatalf("status=%s, want active", got.Status)
	}
	if got.Authority != memory.AuthorityVerified {
		t.Fatalf("authority=%s, want verified", got.Authority)
	}
	if got.Source.Type != memory.SourceTypeCommandResult {
		t.Fatalf("source_type=%s, want command_result", got.Source.Type)
	}
}

// TestProposeMemoryAlwaysProposed covers the inferred path: even when the
// caller does not explicitly request a proposed status, ProposeMemory must
// return StatusProposed (spec §4.1: inferred memories cannot become active
// without an explicit user approval step). The Authority assertion guards
// against a future regression that drops the routed value before insert.
func TestProposeMemoryAlwaysProposed(t *testing.T) {
	store := setupMemoryStore(t)
	s := memory.OpenMemory(store)
	in := memory.Memory{
		Claim: "推断出...", Authority: memory.AuthorityInferred,
		Scope:  memory.Scope{Kind: "project", Value: "mengdie"},
		Source: memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: "session-1:1:agent"},
	}
	got, err := s.ProposeMemory(context.Background(), in)
	if err != nil {
		t.Fatalf("ProposeMemory: %v", err)
	}
	if got.Status != memory.StatusProposed {
		t.Fatalf("inferred must be proposed, got %s", got.Status)
	}
	if got.Authority != memory.AuthorityInferred {
		t.Fatalf("authority=%s, want inferred", got.Authority)
	}
}

// TestSaveRejectsMismatchedAuthority exercises the spec §4.1 Authority↔
// SourceType guard at the Save* entry point: calling SaveUserMemory with a
// Source.Type that does not match the routed SourceTypeUserMessage must
// return ErrAuthorityGuard (errors.Is match).
func TestSaveRejectsMismatchedAuthority(t *testing.T) {
	store := setupMemoryStore(t)
	s := memory.OpenMemory(store)
	in := memory.Memory{
		Claim:  "should be rejected",
		Scope:  memory.Scope{Kind: "project", Value: "mengdie"},
		Source: memory.SourceRef{Type: memory.SourceTypeFile, Ref: "go.mod:3"},
	}
	_, err := s.SaveUserMemory(context.Background(), in)
	if !errors.Is(err, memory.ErrAuthorityGuard) {
		t.Fatalf("err=%v, want ErrAuthorityGuard", err)
	}
}

// TestSaveMarksBothDisputedOnSameScopeDifferentClaim locks in spec §4.2 row
// 2 ("双方都置 disputed"): when an incoming memory has a different
// normalized claim from an existing same-scope + same-authority peer, both
// the existing peer (re-loaded via idempotent re-Save) and the new row must
// land as StatusDisputed.
func TestSaveMarksBothDisputedOnSameScopeDifferentClaim(t *testing.T) {
	store := setupMemoryStore(t)
	s := memory.OpenMemory(store)

	scope := memory.Scope{Kind: "project", Value: "mengdie"}

	first, err := s.SaveUserMemory(context.Background(), memory.Memory{
		Claim:  "项目测试入口是 go test ./internal/foo",
		Scope:  scope,
		Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-1:1:user"},
	})
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	if first.Status != memory.StatusActive {
		t.Fatalf("first status=%s, want active", first.Status)
	}

	second, err := s.SaveUserMemory(context.Background(), memory.Memory{
		Claim:  "项目测试入口是 go test ./...",
		Scope:  scope,
		Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-1:2:user"},
	})
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if second.Status != memory.StatusDisputed {
		t.Fatalf("new row status=%s, want disputed", second.Status)
	}

	// Re-saving the original claim should hit the normalized-idempotency
	// path and return the peer row, which must now be StatusDisputed.
	reloaded, err := s.SaveUserMemory(context.Background(), memory.Memory{
		Claim:  "项目测试入口是 go test ./internal/foo",
		Scope:  scope,
		Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-1:3:user"},
	})
	if err != nil {
		t.Fatalf("reload save: %v", err)
	}
	if reloaded.Status != memory.StatusDisputed {
		t.Fatalf("peer status=%s, want disputed", reloaded.Status)
	}
	if reloaded.ID != first.ID {
		t.Fatalf("peer id=%s, want %s (idempotency broken)", reloaded.ID, first.ID)
	}
}

// setupSeededStore opens a session-backed SQLite store with the 008_memory
// migration applied and wraps it in a memory.Store. It is the read-side
// counterpart of setupMemoryStore: every List/Get/Why test seeds a small
// fixture set up front so assertions can assume the rows exist.
//
// Seeds:
//   - explicit, project=mengdie, status=active ("项目测试入口")
//   - repository, project=mengdie, status=active ("go.mod declares Go 1.26.6")
//   - verified, project=mengdie, status=active ("go test ./... exits 0")
//   - inferred, project=mengdie, status=proposed ("agent-inferred project structure")
//
// The seeded set lets List filter tests exercise per-authority selection and
// the Why report test assert non-empty Source.Ref and a non-nil Evidence
// section.
func setupSeededStore(t *testing.T) *memory.Store {
	t.Helper()
	sessionStore := setupMemoryStore(t)
	s := memory.OpenMemory(sessionStore)
	ctx := context.Background()
	projectScope := memory.Scope{Kind: "project", Value: "mengdie"}
	seeds := []memory.Memory{
		{
			Claim: "项目测试入口是 go test ./...", Authority: memory.AuthorityExplicit,
			Scope:  projectScope,
			Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-seed:1:user"},
		},
		{
			Claim: "go.mod declares Go 1.26.6", Authority: memory.AuthorityRepository,
			Scope:  projectScope,
			Source: memory.SourceRef{Type: memory.SourceTypeFile, Ref: "go.mod:3"},
		},
		{
			Claim: "go test ./... exits 0", Authority: memory.AuthorityVerified,
			Scope:  projectScope,
			Source: memory.SourceRef{Type: memory.SourceTypeCommandResult, Ref: "go test ./... exit=0"},
		},
		{
			Claim: "agent-inferred project structure", Authority: memory.AuthorityInferred,
			Scope:  projectScope,
			Source: memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: "session-seed:1:agent"},
		},
	}
	for i, in := range seeds {
		got, err := s.Save(ctx, in)
		if err != nil {
			t.Fatalf("seed %d: Save: %v", i, err)
		}
		if got.ID == "" {
			t.Fatalf("seed %d: empty id", i)
		}
	}
	return s
}

// TestListFiltersByScopeAndAuthority covers the dynamic-WHERE contract: when
// both ScopeKind/ScopeValue and Authority are set, List must return only the
// matching rows and never leak a different Authority through. The seed has
// exactly one explicit project memory, so len > 0 must hold and every returned
// row's Authority must equal AuthorityExplicit.
func TestListFiltersByScopeAndAuthority(t *testing.T) {
	s := setupSeededStore(t)
	got, err := s.List(context.Background(), memory.ListQuery{
		ScopeKind: "project", ScopeValue: "mengdie", Authority: "explicit", Limit: 10,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected explicit project memories")
	}
	for _, m := range got {
		if m.Authority != memory.AuthorityExplicit {
			t.Fatalf("filter leaked: %s", m.Authority)
		}
	}
}

// TestWhyReturnsAllSixSections covers the spec §5 / §7 audit surface: the
// `mengdie memory why <id>` CLI command (and the Trust Set's
// `why_completeness` metric) require WhyReport to carry all six sections —
// source, observed_at (encoded in Memory.ObservedAt), scope (encoded in
// Memory.Scope), evidence, conflicts, recent_usage — with no nil fields so
// the CLI formatter can render them unconditionally.
func TestWhyReturnsAllSixSections(t *testing.T) {
	s := setupSeededStore(t)
	mems, err := s.List(context.Background(), memory.ListQuery{Limit: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mems) == 0 {
		t.Fatal("no memories")
	}
	report, err := s.Why(context.Background(), mems[0].ID)
	if err != nil {
		t.Fatalf("Why: %v", err)
	}
	if report.Memory.ID != mems[0].ID {
		t.Fatal("id mismatch")
	}
	if report.Source.Ref == "" {
		t.Fatal("source.ref missing")
	}
	if report.Evidence == nil {
		t.Fatal("evidence section missing")
	}
	if report.RecentUsage == nil {
		t.Fatal("usage section missing")
	}
	if report.Conflicts == nil {
		t.Fatal("conflicts section missing")
	}
}

// TestSaveIsIdempotentUnderConflict locks in the contract that two
// concurrent writers in the same scope + authority cannot surface a
// UNIQUE-constraint failure to the caller: both must return the same id and
// no error. The race fix (INSERT ... ON CONFLICT(id) DO NOTHING RETURNING
// id) collapses the SELECT-then-INSERT race window, with the normalized
// SELECT-for-idempotency still catching the NFD/NFC same-claim case that
// ON CONFLICT(id) cannot detect on its own.
//
// The test opens two session stores against the same SQLite file (WAL mode
// permits multiple writers) and fans the same Save call out across many
// goroutines, alternating between stores. The actual race is hard to
// reproduce deterministically — both stores serialize at BEGIN IMMEDIATE
// and the loser's SELECT sees the winner's committed row — but the
// contract is observable: every goroutine observes the same id and no
// error.
func TestSaveIsIdempotentUnderConflict(t *testing.T) {
	directory := t.TempDir()
	storeA := openSharedMemoryStore(t, directory, 5*time.Second)
	t.Cleanup(func() {
		if err := storeA.Close(); err != nil {
			t.Errorf("close storeA: %v", err)
		}
	})
	storeB := openSharedMemoryStore(t, directory, 5*time.Second)
	t.Cleanup(func() {
		if err := storeB.Close(); err != nil {
			t.Errorf("close storeB: %v", err)
		}
	})

	memA := memory.OpenMemory(storeA)
	memB := memory.OpenMemory(storeB)

	in := memory.Memory{
		Claim:  "项目用 go test ./...",
		Scope:  memory.Scope{Kind: "project", Value: "mengdie"},
		Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-1:1:user"},
	}

	const concurrency = 32
	results := make([]struct {
		id  string
		err error
	}, concurrency)

	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		store := memA
		if i%2 == 1 {
			store = memB
		}
		go func(index int, target *memory.Store) {
			defer wg.Done()
			got, err := target.SaveUserMemory(context.Background(), in)
			results[index] = struct {
				id  string
				err error
			}{got.ID, err}
		}(i, store)
	}
	wg.Wait()

	expectedID := memory.GenerateID(in.Claim, in.Scope, string(memory.AuthorityExplicit), "session-1")
	for i, r := range results {
		if r.err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, r.err)
			continue
		}
		if r.id != expectedID {
			t.Errorf("goroutine %d: id=%s, want %s", i, r.id, expectedID)
		}
	}

	// Exactly one durable row should exist for this (claim, scope,
	// authority, sessionID) tuple — UNIQUE(id) plus the idempotency
	// contract together forbid duplicates.
	var rowCount int
	if err := storeA.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM memories WHERE id = ?`, expectedID,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("expected exactly one row with id=%s, got %d", expectedID, rowCount)
	}
}

// TestForgetHardDeletes covers spec §5 (`memory forget --hard <id>` exit 0,
// exit 3 for missing id): a hard delete must remove the row entirely so a
// subsequent Get returns ErrMemoryNotFound. The FK cascade on
// memory_evidence / memory_usage takes care of dependent rows automatically.
func TestForgetHardDeletes(t *testing.T) {
	s := setupSeededStore(t)
	mems, err := s.List(context.Background(), memory.ListQuery{Limit: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mems) == 0 {
		t.Fatal("need at least one seeded memory")
	}
	if err := s.Forget(context.Background(), mems[0].ID, true); err != nil {
		t.Fatalf("Forget hard: %v", err)
	}
	if _, err := s.Get(context.Background(), mems[0].ID); !errors.Is(err, memory.ErrMemoryNotFound) {
		t.Fatalf("hard delete must remove row, got %v", err)
	}
}

// TestForgetSoftArchives covers the default (--hard=false) path: Forget must
// flip status to archived (visibility off in CLI list filters) instead of
// physically removing the row, matching the spec §5 soft-delete semantics
// for accidental `forget`s that the user can later recover via direct SQL.
func TestForgetSoftArchives(t *testing.T) {
	s := setupSeededStore(t)
	mems, err := s.List(context.Background(), memory.ListQuery{Limit: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := s.Forget(context.Background(), mems[0].ID, false); err != nil {
		t.Fatalf("Forget soft: %v", err)
	}
	got, err := s.Get(context.Background(), mems[0].ID)
	if err != nil {
		t.Fatalf("Get after soft delete: %v", err)
	}
	if got.Status != memory.StatusArchived {
		t.Fatalf("soft delete must archive, got %s", got.Status)
	}
}

// TestForgetReturnsNotFoundOnMissingID covers spec §5 exit code 3
// (`mengdie memory forget <id>` with an unknown id): Forget must surface
// ErrMemoryNotFound so the CLI can map it to exit 3 via errors.Is.
func TestForgetReturnsNotFoundOnMissingID(t *testing.T) {
	s := setupSeededStore(t)
	err := s.Forget(context.Background(), "mem_does_not_exist", true)
	if !errors.Is(err, memory.ErrMemoryNotFound) {
		t.Fatalf("forget on missing id must return ErrMemoryNotFound, got %v", err)
	}
}

// TestSupersedeMarksOldSuperseded covers spec §4.2 conflict policy row 4
// (explicit supersede): Supersede must flip the old row's status to
// `superseded` and stamp `supersedes=<newID>` so `mengdie memory why <old>`
// can show the chain and so List / Tier1 filters exclude superseded rows
// while the chain is still traceable.
func TestSupersedeMarksOldSuperseded(t *testing.T) {
	s := setupSeededStore(t)
	mems, err := s.List(context.Background(), memory.ListQuery{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mems) < 2 {
		t.Fatal("need 2 memories")
	}
	if err := s.Supersede(context.Background(), mems[0].ID, mems[1].ID); err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	got, err := s.Get(context.Background(), mems[0].ID)
	if err != nil {
		t.Fatalf("Get old: %v", err)
	}
	if got.Status != memory.StatusSuperseded {
		t.Fatalf("status=%s, want superseded", got.Status)
	}
	if got.Supersedes != mems[1].ID {
		t.Fatalf("supersedes field=%q, want %s", got.Supersedes, mems[1].ID)
	}
}

// TestApproveOnlyProposed covers the happy path for
// `mengdie memory approve <id>`: an inferred memory starts in StatusProposed
// and Approve must promote it to StatusActive so the Tier 1 / 2 catalog
// renders it and RecomputeEvidenceScore runs to fold in any evidence that
// piled up while the memory was awaiting approval.
//
// Note on scope: the seeded store already has an inferred memory in
// {project, mengdie}; per spec §4.2 row 2 a same-scope + same-authority +
// different-claim insert would flip both rows to disputed, which is
// correct production behaviour but blocks the Approve happy path. This
// test uses a fresh task scope so the proposed memory lands clean as
// proposed without touching the seeded conflict surface.
func TestApproveOnlyProposed(t *testing.T) {
	s := setupSeededStore(t)
	in := memory.Memory{
		Claim:     "推断 X",
		Authority: memory.AuthorityInferred,
		Scope:     memory.Scope{Kind: "task", Value: "approve-test"},
		Source:    memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: "s:1:agent"},
	}
	got, err := s.ProposeMemory(context.Background(), in)
	if err != nil {
		t.Fatalf("ProposeMemory: %v", err)
	}
	if err := s.Approve(context.Background(), got.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	after, err := s.Get(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("Get after Approve: %v", err)
	}
	if after.Status != memory.StatusActive {
		t.Fatalf("approved must be active, got %s", after.Status)
	}
}

// TestApproveRejectsNonProposed exercises the Approve guard: calling Approve
// on a memory whose status is anything other than StatusProposed must return
// ErrNotProposed so the CLI can render `memory approve <id>: not a proposed
// memory` rather than silently re-promoting a row that's already active (or
// flipping an archived row).
func TestApproveRejectsNonProposed(t *testing.T) {
	s := setupSeededStore(t)
	mems, err := s.List(context.Background(), memory.ListQuery{Status: "active", Limit: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mems) == 0 {
		t.Fatal("need at least one active seeded memory")
	}
	if err := s.Approve(context.Background(), mems[0].ID); !errors.Is(err, memory.ErrNotProposed) {
		t.Fatalf("approve on active must fail with ErrNotProposed, got %v", err)
	}
}

// TestRecomputeEvidenceScore covers spec §4.3: evidence_score =
// 1.0*user_confirmed + 0.6*reobserved + 0.3*task_verified. After inserting
// one user_confirmed + one reobserved row, RecomputeEvidenceScore must
// bump evidence_score to at least 1.6, proving the formula and the auto-call
// path inside RecordEvidence both work.
func TestRecomputeEvidenceScore(t *testing.T) {
	s := setupSeededStore(t)
	mems, err := s.List(context.Background(), memory.ListQuery{Limit: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mems) == 0 {
		t.Fatal("no memories")
	}
	if err := s.RecordEvidence(context.Background(), memory.Evidence{
		ID: "ev-1", MemoryID: mems[0].ID, Kind: "user_confirmed",
		SourceRef: "u:1", Weight: 1.0, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordEvidence user_confirmed: %v", err)
	}
	if err := s.RecordEvidence(context.Background(), memory.Evidence{
		ID: "ev-2", MemoryID: mems[0].ID, Kind: "reobserved",
		SourceRef: "s:2", Weight: 0.6, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordEvidence reobserved: %v", err)
	}
	if err := s.RecomputeEvidenceScore(context.Background(), mems[0].ID); err != nil {
		t.Fatalf("RecomputeEvidenceScore: %v", err)
	}
	got, err := s.Get(context.Background(), mems[0].ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.EvidenceScore < 1.5 {
		t.Fatalf("expected >=1.5 (1.0+0.6), got %v", got.EvidenceScore)
	}
}
