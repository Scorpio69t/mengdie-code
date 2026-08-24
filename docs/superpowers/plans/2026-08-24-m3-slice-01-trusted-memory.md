# M3 Slice 01 Implementation Plan — Trusted Memory

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 M2 EventStore / Patch Journal 之上落地可信记忆系统的最小可用层：SQLite schema + FTS5 + Authority 守门 + Conflict 状态机 + 9 个 `mengdie memory` 子命令 + 三级召回 + Agent 第一 turn 注入 + Memory Trust Set 30 场景评测。

**Architecture:** 共享 `state.db`（同 session），新增 `008_memory.sql` 迁移。`internal/memory` 包独立，接收 `*session.SQLiteStore`；`internal/memory/store` 实现 Authority 路由的 `Save` 与查询/审计；`internal/memory/retrieve` 实现三级召回；`agent.Options` 新增 `MemoryRetriever` 接口；`internal/app/memory.go` 实现 CLI；`evals/memory/trust-set-v1.json` + runner 提供退出门禁。

**Tech Stack:** Go 1.26+、modernc.org/sqlite（已含）、charm.land/bubbletea/v2（已含）、Go embed.FS for migrations（已用）、testing（已用）。

**Spec:** `docs/superpowers/specs/2026-08-24-m3-slice-01-trusted-memory-design.md`
**Beads:** `mengdie-n47`

## Global Constraints

- Go 1.26.6，模块路径 `github.com/Scorpio69t/mengdie-code`
- 仅 `CGO_ENABLED=0` 跨平台构建；四目标必须通过：darwin/arm64、darwin/amd64、windows/amd64、linux/amd64
- 禁止在用户仓库中自动 git commit / push；任何 `git commit` 都由执行人显式触发
- 不修改 `internal/session/migrations/00{1..7}*.sql` 既有迁移；只新增
- `evidence_score` 字段只能由 `Store.RecomputeEvidenceScore` 重算；LLM 写入只填 `confidence`
- 任何含凭据 / 任务正文 / 用户代码的输出视为失败
- 现有 `internal/session/service.go` 与 `internal/agent/runtime.go` 的导出 API 不破坏；新功能以 `MemoryRetriever` 接口 + 新 Options 字段方式插入
- 错误统一用 `errors.Is/Join`；中文错误信息按现有仓库风格

## File Structure

```
新增：
internal/memory/memory.go                 # Memory, Authority, Scope, Status, SourceRef, Evidence
internal/memory/store.go                  # Store: Save (4 路由) + List/Get/Why/Forget/Supersede/Approve/RecordEvidence/RecordUsage/RecomputeEvidenceScore
internal/memory/store_test.go
internal/memory/retrieve.go               # Retriever, 3-level recall, scoring
internal/memory/retrieve_test.go
internal/memory/trustset/runner.go        # Trust Set runner
internal/memory/trustset/runner_test.go
internal/session/migrations/008_memory.sql
internal/tools/memory_recall.go           # memory_recall tool (effect=state)
internal/tools/memory_recall_test.go
internal/app/memory.go                    # 9 个 CLI 子命令
internal/app/memory_test.go
internal/app/agent_memory_integration_test.go
internal/app/agent_memory_test_helpers_test.go
evals/memory/trust-set-v1.json            # 30 场景
docs/development/phase-3-slice-01/IMPLEMENTATION_REPORT.md
.github/workflows/memory-live-provider.yml

修改：
internal/session/sqlite_store.go           # 加 OpenMemory(store) 工厂
internal/agent/runtime.go                  # Options 新增 MemoryRetriever / ProjectIdentity; 第一个 turn 前注入召回
cmd/mengdie/main.go                        # 注册 memory 子命令
.github/workflows/ci.yml                   # 加 memory 步骤
README.md                                  # 标记 M3 Slice 01 完成
```

## Interfaces Introduced (across all tasks)

```go
// internal/memory/memory.go
type Authority string
const (
    AuthorityExplicit   Authority = "explicit"
    AuthorityRepository Authority = "repository"
    AuthorityVerified   Authority = "verified"
    AuthorityInferred   Authority = "inferred"
)
type Scope struct{ Kind string; Value string }  // "user"|"project"|"branch"|"task"
type Status string
const (
    StatusProposed   Status = "proposed"
    StatusActive     Status = "active"
    StatusStale      Status = "stale"
    StatusDisputed   Status = "disputed"
    StatusSuperseded Status = "superseded"
    StatusArchived   Status = "archived"
)
type SourceType string
type SourceRef struct{ Type SourceType; Ref string }
type Memory struct {
    ID, Claim, Kind string
    Scope Scope
    Authority Authority
    Source SourceRef
    ObservedAt time.Time
    ValidFrom, ValidUntil *time.Time
    Status Status
    Confidence float64
    EvidenceScore float64
    Supersedes string
}
type Evidence struct {
    ID, MemoryID, Kind string  // "user_confirmed"|"reobserved"|"task_verified"
    SourceRef string
    Weight float64
    CreatedAt time.Time
}
type UsageRecord struct {
    MemoryID, SessionID string
    RecalledAt time.Time
    Outcome string  // "unknown"|"helpful"|"harmful"|"unused"
}

// internal/memory/store.go
type Store struct { /* wraps *session.SQLiteStore */ }
func OpenMemory(store *session.SQLiteStore) *Store
func (s *Store) Save(ctx context.Context, m Memory) (Memory, error)
func (s *Store) SaveUserMemory(ctx context.Context, m Memory) (Memory, error)
func (s *Store) SaveRepositoryFact(ctx context.Context, m Memory) (Memory, error)
func (s *Store) SaveVerifiedFact(ctx context.Context, m Memory) (Memory, error)
func (s *Store) ProposeMemory(ctx context.Context, m Memory) (Memory, error)
func (s *Store) List(ctx context.Context, q ListQuery) ([]Memory, error)
func (s *Store) Get(ctx context.Context, id string) (Memory, error)
func (s *Store) Why(ctx context.Context, id string) (WhyReport, error)
func (s *Store) Forget(ctx context.Context, id string, hard bool) error
func (s *Store) Supersede(ctx context.Context, oldID, newID string) error
func (s *Store) Approve(ctx context.Context, id string) error
func (s *Store) RecordEvidence(ctx context.Context, ev Evidence) error
func (s *Store) RecordUsage(ctx context.Context, rec UsageRecord) error
func (s *Store) RecomputeEvidenceScore(ctx context.Context, memoryID string) error
func (s *Store) Rebuild(ctx context.Context) error
type ListQuery struct {
    ScopeKind, ScopeValue, Authority, Status string
    Limit int
}
type WhyReport struct {
    Memory Memory
    Source SourceRef
    Evidence []Evidence
    Conflicts []Memory  // disputed / superseded 双方
    RecentUsage []UsageRecord  // 最多 5 条
}

// internal/memory/retrieve.go
type Retriever struct{ store *Store }
func NewRetriever(store *Store) *Retriever
func (r *Retriever) Tier1Catalogue(ctx context.Context, scope Scope, limit int) ([]CatalogueEntry, error)
func (r *Retriever) Tier2TaskTopics(ctx context.Context, scope Scope) ([]Memory, error)
func (r *Retriever) Tier3AtomicRecall(ctx context.Context, query string, topK int, scope Scope) ([]RecallHit, error)
type CatalogueEntry struct {
    ID, Claim string
    EvidenceScore float64
}
type RecallHit struct {
    Memory
    Score float64
}

// internal/agent/runtime.go (新增)
type MemoryRetriever interface {
    Tier1Catalogue(ctx context.Context, scope MemoryScope, limit int) ([]CatalogueEntry, error)
    Tier3AtomicRecall(ctx context.Context, query string, topK int, scope MemoryScope) ([]RecallHit, error)
}
type MemoryScope struct {
    Kind, Value, ProjectIdentity string
}
type Options struct {
    // ... 既有字段 ...
    MemoryRetriever MemoryRetriever
    ProjectIdentity string
}

// internal/tools/memory_recall.go
type memoryRecallTool struct{ retriever MemoryRetriever }
func NewMemoryRecallTool(retriever MemoryRetriever) tools.Tool
// Input: { "query": "...", "topK": 5 }
// Output: markdown bullet list of {id, claim, source_ref}

// internal/memory/trustset/runner.go
type Scenario struct { /* 见 spec §7 */ }
type Result struct { /* precision@5, false_recall_rate, etc. */ }
func Run(ctx context.Context, store *Store, retriever *Retriever, scenarios []Scenario, outPath string) (Result, error)
```

---

## Task 1: Memory 迁移 008_memory.sql

**Files:**
- Create: `internal/session/migrations/008_memory.sql`
- Test: `internal/session/migrations_test.go` (新)

**Interfaces:**
- Consumes: existing `embeddedMigrations` in `internal/session/migrations.go`
- Produces: migration `008_memory` with checksum

- [ ] **Step 1: Write the failing migration test**

Add to `internal/session/migrations_test.go`:

```go
func TestMigration008MemoryApplied(t *testing.T) {
    store, err := session.OpenSQLite(context.Background(), session.OpenOptions{
        DataDir: t.TempDir(), ProjectRoot: t.TempDir(), Now: time.Now,
    })
    if err != nil { t.Fatal(err) }
    defer store.Close()
    rows, err := store.DB().QueryContext(context.Background(),
        `SELECT name FROM sqlite_master WHERE type='table' AND name IN ('memories','memories_fts','memory_evidence','memory_usage')`)
    if err != nil { t.Fatal(err) }
    defer rows.Close()
    seen := map[string]bool{}
    for rows.Next() {
        var n string
        if err := rows.Scan(&n); err != nil { t.Fatal(err) }
        seen[n] = true
    }
    for _, want := range []string{"memories","memories_fts","memory_evidence","memory_usage"} {
        if !seen[want] { t.Fatalf("missing %s", want) }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session -run TestMigration008MemoryApplied -count=1`
Expected: FAIL (tables don't exist)

- [ ] **Step 3: Create the migration file**

Write `internal/session/migrations/008_memory.sql` exactly as the spec §3. Include CREATE TABLE memories (with UNIQUE id, CHECK constraints for kind/authority/scope_kind/status), 3 indexes, CREATE VIRTUAL TABLE memories_fts (claim, content='memories', content_rowid='rowid'), 3 triggers (memories_ai/ad/au), CREATE TABLE memory_evidence, CREATE TABLE memory_usage, and 2 indexes on usage/evidence.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/session -run TestMigration008MemoryApplied -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/session/migrations/008_memory.sql internal/session/migrations_test.go
git commit -m "feat(memory): add 008_memory migration with FTS5 + triggers"
```

---

## Task 2: Memory types (Authority, Scope, Status, Memory, Evidence, SourceRef, UsageRecord)

**Files:**
- Create: `internal/memory/memory.go`
- Test: `internal/memory/memory_test.go` (新)

**Interfaces:**
- Consumes: spec §3 schema types; spec §4.1 Authority 守门表
- Produces: `Memory`, `Authority`, `Scope`, `Status`, `SourceType`, `SourceRef`, `Evidence`, `UsageRecord`, `GenerateID()` (sha256-based 12-byte id with "mem_" prefix)

- [ ] **Step 1: Write the failing type test**

```go
func TestAuthorityValues(t *testing.T) {
    want := []string{"explicit","repository","verified","inferred"}
    for _, w := range want {
        if string(memory.Authority(w)) != w {
            t.Fatalf("Authority(%q) round-trip failed", w)
        }
    }
}
func TestGenerateIDStable(t *testing.T) {
    a := memory.GenerateID("claim-X", memory.Scope{Kind:"project", Value:"mengdie"}, "explicit", "session-1")
    b := memory.GenerateID("claim-X", memory.Scope{Kind:"project", Value:"mengdie"}, "explicit", "session-1")
    if a != b { t.Fatal("same input must produce same id") }
    if !strings.HasPrefix(a, "mem_") { t.Fatal("id must start with mem_") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/memory -count=1`
Expected: FAIL (package doesn't compile)

- [ ] **Step 3: Implement the types**

In `internal/memory/memory.go`:
- Package doc citing spec §3, §4
- Type `Authority string` + 4 consts (AuthorityExplicit, AuthorityRepository, AuthorityVerified, AuthorityInferred)
- Type `Scope struct{Kind, Value string}` with `Valid()` method (kind ∈ {user, project, branch, task})
- Type `Status string` + 6 consts
- Type `SourceType string` + 5 consts (SourceTypeUserMessage, SourceTypeAgentMessage, SourceTypeSessionEvent, SourceTypeFile, SourceTypeCommandResult)
- Type `SourceRef struct{Type SourceType; Ref string}` with `Valid()` method
- Type `Memory struct` with all spec §3 fields
- Type `Evidence struct{ID, MemoryID, Kind, SourceRef string; Weight float64; CreatedAt time.Time}`
- Type `UsageRecord struct{MemoryID, SessionID string; RecalledAt time.Time; Outcome string}`
- Func `GenerateID(claim string, scope Scope, authority string, sessionID string) string` — `mem_` + sha256(claim||scope||authority||sessionID)[:16]

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/memory -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/memory/memory.go internal/memory/memory_test.go
git commit -m "feat(memory): add Memory/Authority/Scope/Status types"
```

---

## Task 3: Store + Authority-routed Save

**Files:**
- Create: `internal/memory/store.go`
- Modify: `internal/session/sqlite_store.go` (add `DB()` accessor for test/direct use)
- Test: `internal/memory/store_test.go`

**Interfaces:**
- Consumes: `Memory` from task 2; spec §3 schema; spec §4.1 Authority 守门
- Produces: `OpenMemory`, `Save` (4 路由：SaveUserMemory / SaveRepositoryFact / SaveVerifiedFact / ProposeMemory)

- [ ] **Step 1: Write the failing store test**

```go
func TestSaveUserMemoryCreatesActive(t *testing.T) {
    store := setupMemoryStore(t)
    s := memory.OpenMemory(store)
    in := memory.Memory{
        Claim: "项目用 go test ./...", Authority: memory.AuthorityExplicit,
        Scope: memory.Scope{Kind: "project", Value: "mengdie"},
        Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-1:1:user"},
    }
    got, err := s.SaveUserMemory(context.Background(), in)
    if err != nil { t.Fatal(err) }
    if got.Status != memory.StatusActive { t.Fatalf("status=%s", got.Status) }
    if got.ID == "" { t.Fatal("id empty") }
}
func TestProposeMemoryAlwaysProposed(t *testing.T) {
    store := setupMemoryStore(t)
    s := memory.OpenMemory(store)
    in := memory.Memory{
        Claim: "推断出...", Authority: memory.AuthorityInferred,
        Scope: memory.Scope{Kind: "project", Value: "mengdie"},
        Source: memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: "session-1:1:agent"},
    }
    got, _ := s.ProposeMemory(context.Background(), in)
    if got.Status != memory.StatusProposed { t.Fatalf("inferred must be proposed") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/memory -run "TestSaveUserMemoryCreatesActive|TestProposeMemoryAlwaysProposed" -count=1`
Expected: FAIL (package doesn't compile)

- [ ] **Step 3: Add `DB()` accessor to SQLiteStore**

In `internal/session/sqlite_store.go`, add:
```go
// DB exposes the underlying *sql.DB for subsystems that need direct query
// access (e.g., the memory package's FTS5 + index joins). Callers must
// not modify the schema or open separate transactions.
func (s *SQLiteStore) DB() *sql.DB { return s.db }
```

- [ ] **Step 4: Implement OpenMemory and Save methods**

In `internal/memory/store.go`:
- `type Store struct { db *sql.DB; now func() time.Time }`
- `OpenMemory(store *session.SQLiteStore) *Store` (uses `store.DB()`)
- `Save(ctx, m) (Memory, error)` — switch on `m.Authority`:
  - `AuthorityExplicit` → call `SaveUserMemory` (forces `Status=active`, requires `Source.Type=SourceTypeUserMessage`)
  - `AuthorityRepository` → `SaveRepositoryFact` (forces active, requires `Source.Type=SourceTypeFile`)
  - `AuthorityVerified` → `SaveVerifiedFact` (forces active, requires `Source.Type=SourceTypeCommandResult`)
  - `AuthorityInferred` → `ProposeMemory` (forces `Status=proposed`, requires `Source.Type=SourceTypeAgentMessage`)
- All four check `m.Scope.Valid()` and `m.Source.Valid()`; return `ErrInvalidMemory` otherwise
- Idempotency: query existing memory by `(scope_kind, scope_value, claim_normalized)`; if exists, return that one (no error)
- Claim normalization: `strings.EqualFold` + `norm.NFD.String` then `norm.NFC.String`
- `Conflict` insert: on save, if same scope has different-claim same-authority, also `disputed` the existing memory (set status='disputed', updated_at=now)
- Insert: `INSERT INTO memories (id, claim, kind, scope_kind, scope_value, authority, source_type, source_ref, observed_at, valid_from, valid_until, status, confidence, evidence_score, supersedes, created_at, updated_at) VALUES (...)`

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/memory -run "TestSaveUserMemoryCreatesActive|TestProposeMemoryAlwaysProposed" -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/memory/store.go internal/memory/store_test.go internal/session/sqlite_store.go
git commit -m "feat(memory): add Store with Authority-routed Save"
```

---

## Task 4: Store query/audit: List / Get / Why

**Files:**
- Modify: `internal/memory/store.go`
- Modify: `internal/memory/store_test.go`

**Interfaces:**
- Consumes: `Memory`, `Evidence`, `UsageRecord`, `ListQuery`, `WhyReport` from tasks 2-3; spec §5 CLI 表面与 §7 `why` 6 段
- Produces: `List`, `Get`, `Why`

- [ ] **Step 1: Write the failing test**

```go
func TestListFiltersByScopeAndAuthority(t *testing.T) {
    s := setupSeededStore(t)
    got, err := s.List(context.Background(), memory.ListQuery{ScopeKind:"project", ScopeValue:"mengdie", Authority:"explicit", Limit:10})
    if err != nil { t.Fatal(err) }
    if len(got) == 0 { t.Fatal("expected explicit project memories") }
    for _, m := range got {
        if m.Authority != memory.AuthorityExplicit { t.Fatalf("filter leaked: %s", m.Authority) }
    }
}
func TestWhyReturnsAllSixSections(t *testing.T) {
    s := setupSeededStore(t)
    mems, _ := s.List(context.Background(), memory.ListQuery{Limit:1})
    if len(mems) == 0 { t.Fatal("no memories") }
    report, err := s.Why(context.Background(), mems[0].ID)
    if err != nil { t.Fatal(err) }
    if report.Memory.ID != mems[0].ID { t.Fatal("id mismatch") }
    if report.Source.Ref == "" { t.Fatal("source.ref missing") }
    if report.Evidence == nil { t.Fatal("evidence section missing") }
    if report.RecentUsage == nil { t.Fatal("usage section missing") }
    if report.Conflicts == nil { t.Fatal("conflicts section missing") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/memory -run "TestListFiltersByScopeAndAuthority|TestWhyReturnsAllSixSections" -count=1`
Expected: FAIL

- [ ] **Step 3: Implement List**

In `internal/memory/store.go`:
- `List(ctx, q) ([]Memory, error)` — build dynamic WHERE clause from non-empty `q.ScopeKind`, `q.ScopeValue`, `q.Authority`, `q.Status`; `LIMIT q.Limit` (default 20, max 200); `ORDER BY evidence_score DESC, observed_at DESC`

- [ ] **Step 4: Implement Get**

`Get(ctx, id) (Memory, error)` — single row SELECT by id; return `ErrMemoryNotFound` if not found

- [ ] **Step 5: Implement Why**

`Why(ctx, id) (WhyReport, error)`:
1. Load memory via `Get`
2. Load evidence: `SELECT * FROM memory_evidence WHERE memory_id=? ORDER BY created_at DESC`
3. Load usage: `SELECT * FROM memory_usage WHERE memory_id=? ORDER BY recalled_at DESC LIMIT 5`
4. Load conflicts: `SELECT * FROM memories WHERE scope_kind=? AND scope_value=? AND id != ? AND (status='disputed' OR (supersedes=? OR id IN (SELECT supersedes FROM memories WHERE id=?)))`
5. Return `WhyReport{Memory, Source, Evidence, Conflicts, RecentUsage}`

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/memory -run "TestListFiltersByScopeAndAuthority|TestWhyReturnsAllSixSections" -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/memory/store.go internal/memory/store_test.go
git commit -m "feat(memory): add List, Get, Why query/audit API"
```

---

## Task 5: Store mutation: Forget / Supersede / Approve / RecordEvidence / RecordUsage / RecomputeEvidenceScore

**Files:**
- Modify: `internal/memory/store.go`
- Modify: `internal/memory/store_test.go`

**Interfaces:**
- Consumes: spec §4.2 conflict policy, §4.3 evidence_score 公式, §5 CLI exit codes
- Produces: 6 mutation methods + `Rebuild`

- [ ] **Step 1: Write the failing test**

```go
func TestForgetHardDeletes(t *testing.T) {
    s := setupSeededStore(t)
    mems, _ := s.List(context.Background(), memory.ListQuery{Limit:1})
    if err := s.Forget(context.Background(), mems[0].ID, true); err != nil { t.Fatal(err) }
    if _, err := s.Get(context.Background(), mems[0].ID); !errors.Is(err, memory.ErrMemoryNotFound) {
        t.Fatalf("hard delete must remove row, got %v", err)
    }
}
func TestSupersedeMarksOldSuperseded(t *testing.T) {
    s := setupSeededStore(t)
    mems, _ := s.List(context.Background(), memory.ListQuery{Limit:2})
    if len(mems) < 2 { t.Fatal("need 2 memories") }
    if err := s.Supersede(context.Background(), mems[0].ID, mems[1].ID); err != nil { t.Fatal(err) }
    got, _ := s.Get(context.Background(), mems[0].ID)
    if got.Status != memory.StatusSuperseded { t.Fatalf("status=%s", got.Status) }
    if got.Supersedes != mems[1].ID { t.Fatal("supersedes field not set") }
}
func TestApproveOnlyProposed(t *testing.T) {
    s := setupSeededStore(t)
    in := memory.Memory{Claim: "推断 X", Authority: memory.AuthorityInferred, Scope: memory.Scope{Kind:"project",Value:"mengdie"}, Source: memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref:"s:1:agent"}}
    got, _ := s.ProposeMemory(context.Background(), in)
    if err := s.Approve(context.Background(), got.ID); err != nil { t.Fatal(err) }
    after, _ := s.Get(context.Background(), got.ID)
    if after.Status != memory.StatusActive { t.Fatalf("approved must be active") }
}
func TestApproveRejectsNonProposed(t *testing.T) {
    s := setupSeededStore(t)
    mems, _ := s.List(context.Background(), memory.ListQuery{Status: "active", Limit:1})
    if err := s.Approve(context.Background(), mems[0].ID); !errors.Is(err, memory.ErrNotProposed) {
        t.Fatalf("approve on active must fail with ErrNotProposed, got %v", err)
    }
}
func TestRecomputeEvidenceScore(t *testing.T) {
    s := setupSeededStore(t)
    mems, _ := s.List(context.Background(), memory.ListQuery{Limit:1})
    if err := s.RecordEvidence(context.Background(), memory.Evidence{ID: "ev-1", MemoryID: mems[0].ID, Kind: "user_confirmed", SourceRef: "u:1", Weight: 1.0, CreatedAt: time.Now()}); err != nil { t.Fatal(err) }
    if err := s.RecordEvidence(context.Background(), memory.Evidence{ID: "ev-2", MemoryID: mems[0].ID, Kind: "reobserved", SourceRef: "s:2", Weight: 0.6, CreatedAt: time.Now()}); err != nil { t.Fatal(err) }
    if err := s.RecomputeEvidenceScore(context.Background(), mems[0].ID); err != nil { t.Fatal(err) }
    got, _ := s.Get(context.Background(), mems[0].ID)
    if got.EvidenceScore < 1.5 { t.Fatalf("expected >=1.5 (1.0+0.6), got %v", got.EvidenceScore) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/memory -run "TestForgetHardDeletes|TestSupersede|TestApprove|TestRecomputeEvidenceScore" -count=1`
Expected: FAIL

- [ ] **Step 3: Implement Forget / Supersede / Approve**

- `Forget(ctx, id, hard bool)`:
  - if `hard`: `DELETE FROM memories WHERE id=?`; cascade via FK
  - else: `UPDATE memories SET status='archived', updated_at=? WHERE id=?`
- `Supersede(ctx, oldID, newID)`:
  - Load both; verify same scope
  - `UPDATE memories SET status='superseded', supersedes=?, updated_at=? WHERE id=?`
- `Approve(ctx, id)`:
  - Load; if `Status != StatusProposed`: return `ErrNotProposed`
  - `UPDATE memories SET status='active', updated_at=? WHERE id=?`
  - Auto-call `RecomputeEvidenceScore`

- [ ] **Step 4: Implement RecordEvidence / RecordUsage / RecomputeEvidenceScore**

- `RecordEvidence(ctx, ev)`:
  - Validate `ev.Kind ∈ {"user_confirmed","reobserved","task_verified"}`
  - `INSERT INTO memory_evidence ...`
  - Auto-call `RecomputeEvidenceScore(ctx, ev.MemoryID)`
- `RecordUsage(ctx, rec)`:
  - Validate `rec.Outcome ∈ {"unknown","helpful","harmful","unused"}`
  - `INSERT OR IGNORE INTO memory_usage ...`
- `RecomputeEvidenceScore(ctx, memoryID)`:
  - `SELECT kind, COUNT(*) FROM memory_evidence WHERE memory_id=? GROUP BY kind`
  - Compute: `score = 1.0*user_confirmed + 0.6*reobserved + 0.3*task_verified`
  - `UPDATE memories SET evidence_score=?, updated_at=? WHERE id=?`

- [ ] **Step 5: Implement Rebuild**

`Rebuild(ctx)`:
- `PRAGMA memories_fts('rebuild');`
- `UPDATE schema_checkpoints SET last_rebuild_at=?` (optional)

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/memory -run "TestForgetHardDeletes|TestSupersede|TestApprove|TestRecomputeEvidenceScore" -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/memory/store.go internal/memory/store_test.go
git commit -m "feat(memory): add Store mutation API + evidence_score 累计"
```

---

## Task 6: Retriever — 3-level recall + scoring

**Files:**
- Create: `internal/memory/retrieve.go`
- Test: `internal/memory/retrieve_test.go`

**Interfaces:**
- Consumes: `Store` (task 5), `CatalogueEntry`, `RecallHit` from spec §6.1
- Produces: `Retriever` with `Tier1Catalogue`, `Tier2TaskTopics`, `Tier3AtomicRecall`

- [ ] **Step 1: Write the failing retriever test**

```go
func TestTier1CatalogueFiltersStale(t *testing.T) {
    s := setupSeededStore(t)
    r := memory.NewRetriever(s)
    entries, err := r.Tier1Catalogue(context.Background(), memory.Scope{Kind:"project",Value:"mengdie"}, 20)
    if err != nil { t.Fatal(err) }
    for _, e := range entries {
        if e.Claim == "" { t.Fatal("empty claim") }
    }
}
func TestTier3AtomicRecallScoresByFormula(t *testing.T) {
    s := setupSeededStore(t)
    r := memory.NewRetriever(s)
    hits, err := r.Tier3AtomicRecall(context.Background(), "test", 5, memory.Scope{Kind:"project",Value:"mengdie"})
    if err != nil { t.Fatal(err) }
    for _, h := range hits {
        if h.Score <= 0 { t.Fatalf("score must be positive, got %v", h.Score) }
    }
}
func TestTier3RecordsUsage(t *testing.T) {
    s := setupSeededStore(t)
    r := memory.NewRetriever(s)
    _, err := r.Tier3AtomicRecall(context.Background(), "test", 3, memory.Scope{Kind:"project",Value:"mengdie"})
    if err != nil { t.Fatal(err) }
    usages, _ := s.DB().Query("SELECT COUNT(*) FROM memory_usage")
    var count int
    _ = usages.Scan(&count)
    if count == 0 { t.Fatal("usage must be recorded") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/memory -run "TestTier1Catalogue|TestTier3Atomic|TestTier3RecordsUsage" -count=1`
Expected: FAIL

- [ ] **Step 3: Implement Tier1Catalogue and Tier2TaskTopics**

In `internal/memory/retrieve.go`:
- `type Retriever struct { store *Store }`
- `NewRetriever(store *Store) *Retriever`
- `Tier1Catalogue(ctx, scope, limit int) ([]CatalogueEntry, error)`:
  - Query active memories in scope (and scope-ancestor: project < branch < task)
  - Order by `evidence_score DESC, observed_at DESC`
  - Limit; return `(id, truncate(claim, 60), evidence_score)`
- `Tier2TaskTopics` — alias for Tier1Catalogue with default limit 20 (same impl for v0.1)

- [ ] **Step 4: Implement Tier3AtomicRecall with scoring formula**

`Tier3AtomicRecall(ctx, query, topK, scope)`:
1. FTS5 search: `SELECT rowid FROM memories_fts WHERE claim MATCH ? ORDER BY rank LIMIT ?*3` (3x for post-filter)
2. Load Memory rows
3. Filter: `status='active' AND (valid_until IS NULL OR valid_until > now)`
4. For each: compute score
5. Sort desc, take topK
6. Insert memory_usage rows (idempotent)
7. Return

Scoring formula:
```go
authorityWeight := map[Authority]float64{
    AuthorityExplicit: 1.0, AuthorityVerified: 0.8,
    AuthorityRepository: 0.6, AuthorityInferred: 0.3,
}[m.Authority]
scopeMatch := 0.0
if m.Scope.Kind == "project" && scope.Kind == "project" && m.Scope.Value == scope.Value {
    scopeMatch = 0.5
}
conflictPenalty := 0.0
if m.Status == StatusDisputed { conflictPenalty = 0.5 }
if m.Status == StatusStale { conflictPenalty = 0.3 }
score := -bm25 + authorityWeight + m.EvidenceScore + scopeMatch - conflictPenalty
```

(bm25 negative because FTS5 rank is negative-better; use `-rowid_match_score` as a simple proxy or just use authority + evidence for v0.1)

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/memory -run "TestTier1Catalogue|TestTier3Atomic|TestTier3RecordsUsage" -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/memory/retrieve.go internal/memory/retrieve_test.go
git commit -m "feat(memory): add 3-level retriever with scoring formula"
```

---

## Task 7: Agent integration — `MemoryRetriever` field + first-turn injection

**Files:**
- Modify: `internal/agent/runtime.go`
- Test: `internal/app/agent_memory_integration_test.go`
- Create: `internal/app/agent_memory_test_helpers_test.go` (stub Provider)

**Interfaces:**
- Consumes: `Retriever` (task 6), `MemoryScope` (spec §6.2), spec §6.2 first-turn injection
- Produces: `agent.Options.MemoryRetriever` + `ProjectIdentity` + system-prompt injection

- [ ] **Step 1: Write the failing agent integration test**

```go
func TestAgentFirstTurnReceivesMemoryCatalogue(t *testing.T) {
    store := setupMemoryStoreWithSeeds(t)
    retriever := memory.NewRetriever(memory.OpenMemory(store))
    stub := &stubProvider{responses: []provider.ChatResponse{
        {Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
    }}
    agent, _ := agent.New(agent.Options{
        Provider: stub, Registry: setupTestRegistry(t), Guard: setupTestGuard(t),
        Policy: setupTestPolicy(t), Broker: autoApproveBroker{},
        Now: time.Now, MaxContextTokens: 4096,
        MemoryRetriever: retriever, ProjectIdentity: "mengdie-test",
    })
    _, err := agent.Run(context.Background(), agent.RunRequest{
        RunID: "r1", Task: "测试", Model: "stub", DisplayModel: "stub", MaxTurns: 1, Security: "controlled",
    }, setupEmitter(t))
    if err != nil { t.Fatal(err) }
    if len(stub.calls) == 0 { t.Fatal("provider not called") }
    req := stub.calls[0]
    if !strings.Contains(req.SystemContent(), "memory") {
        t.Fatal("first turn must include memory catalogue section in system")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app -run TestAgentFirstTurnReceivesMemoryCatalogue -count=1`
Expected: FAIL

- [ ] **Step 3: Add `MemoryRetriever` and `ProjectIdentity` to `agent.Options`**

In `internal/agent/runtime.go`, add fields to `Options`:
```go
type Options struct {
    // ... 既有字段 ...
    MemoryRetriever MemoryRetriever
    ProjectIdentity string
}
// MemoryRetriever is a one-method surface: the agent asks "what memories
// are relevant for this scope + query" and the retriever decides internally
// which tier (catalogue, task topics, or atomic recall) to serve. The
// 3-tier logic is an implementation detail of *memory.Retriever.
type MemoryRetriever interface {
    Recall(ctx context.Context, query string, topK int, scope MemoryScope) ([]MemoryHit, error)
}
type MemoryScope struct { Kind, Value, ProjectIdentity string }
// MemoryHit is a re-exported alias for memory.RecallHit to keep agent
// self-contained while preserving a single source of truth in memory.
type MemoryHit = struct {
    ID, Claim, Authority string
    EvidenceScore float64
    Score float64
    SourceRef string
}
```

`internal/agent` may import `internal/memory` for the alias because `internal/memory` does not import `internal/agent` (no cycle). The first-turn rendering builds the catalogue section by calling `MemoryRetriever.Recall(ctx, task, 5, scope)` and formatting the returned hits; the catalogue "tier" is realized by the retriever internally lowering topK to capture broader hits.

- [ ] **Step 4: Implement first-turn injection**

In `Agent.Run`, before the first turn's `builder.Build`:
```go
if a.memoryRetriever != nil && state.Turn == 0 && request.Recovery == nil {
    hits, _ := a.memoryRetriever.Recall(ctx, request.Task, 5, MemoryScope{
        Kind: "project", Value: a.projectIdentity, ProjectIdentity: a.projectIdentity,
    })
    if len(hits) > 0 {
        catalogue := renderMemoryCatalogue(hits)
        chatRequest = injectMemoryCatalogue(chatRequest, catalogue)
    }
}
```

Where `injectMemoryCatalogue` appends a markdown section to the system message. Do NOT modify `state.Messages` or the private context log. Do NOT inject on resume (Recovery != nil) — the resumed run already has the original context.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/app -run TestAgentFirstTurnReceivesMemoryCatalogue -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/agent/runtime.go internal/app/agent_memory_integration_test.go internal/app/agent_memory_test_helpers_test.go
git commit -m "feat(memory): integrate retriever into agent first turn"
```

---

## Task 8: `memory_recall` tool

**Files:**
- Create: `internal/tools/memory_recall.go`
- Test: `internal/tools/memory_recall_test.go`

**Interfaces:**
- Consumes: `agent.MemoryRetriever` (task 7)
- Produces: `tools.Tool` named "memory_recall" with `effect=state`

- [ ] **Step 1: Write the failing test**

```go
func TestMemoryRecallToolExecutes(t *testing.T) {
    store := setupMemoryStoreWithSeeds(t)
    retriever := memory.NewRetriever(memory.OpenMemory(store))
    tool := tools.NewMemoryRecallTool(retriever)
    result, err := tool.Execute(context.Background(), &tools.PreparedCall{
        ToolName: "memory_recall", ID: "t-1",
        CanonicalArg: []byte(`{"query":"test","topK":3}`),
        Effects: []tools.Effect{tools.EffectState},
        Digest: tools.ComputeDigest("memory_recall", []byte(`{"query":"test","topK":3}`)),
    }, tools.Capability{}, tools.ExecEnv{Guard: setupTestGuard(t)})
    if err != nil { t.Fatal(err) }
    if !strings.Contains(result.Output, "mem_") { t.Fatalf("expected memory id, got %s", result.Output) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools -run TestMemoryRecallToolExecutes -count=1`
Expected: FAIL

- [ ] **Step 3: Implement the tool**

In `internal/tools/memory_recall.go`:
- `type memoryRecallTool struct { retriever agent.MemoryRetriever }` (or accept a `func(query string, topK int) ([]string, error)` to decouple)
- `NewMemoryRecallTool(retriever)` constructor
- `Spec()` returns `ToolSpec{Name:"memory_recall", Effect:[]Effect{EffectState}, InputSchema: <json schema>}`
- `Prepare(ctx, raw, env)` validates input shape `{query: string, topK?: int}`; topK default 5
- `Execute(ctx, call, cap, env)`:
  - No capability required (state effect)
  - Call retriever Tier3AtomicRecall
  - Format as markdown bullet list: `- {id} (authority={a}, evidence={e:.2f}) {claim} [src: {ref}]`
  - Return `&ToolResult{Output: <list>, Metadata: {"query":..., "count":N}}`

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools -run TestMemoryRecallToolExecutes -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tools/memory_recall.go internal/tools/memory_recall_test.go
git commit -m "feat(memory): add memory_recall tool with tier-3 retrieval"
```

---

## Task 9: `mengdie memory` CLI — 9 subcommands

**Files:**
- Create: `internal/app/memory.go`
- Test: `internal/app/memory_test.go`
- Modify: `cmd/mengdie/main.go`

**Interfaces:**
- Consumes: `Store`, `Retriever` (tasks 5-6); spec §5 CLI 表面与退出码
- Produces: 9 subcommands registered in `cmd/mengdie`

- [ ] **Step 1: Write the failing CLI test**

```go
func TestMemoryRememberAndListRoundTrip(t *testing.T) {
    state := setupAppTestState(t)
    code := runApp(state, []string{"memory", "remember", "用 go test ./...", "--scope", "project"})
    if code != 0 { t.Fatalf("remember exit=%d", code) }
    code = runApp(state, []string{"memory", "list", "--scope", "project"})
    if code != 0 { t.Fatalf("list exit=%d", code) }
    if !strings.Contains(state.stdout.String(), "用 go test ./...") { t.Fatal("list must show remembered claim") }
}
func TestMemoryRejectsUnknownAuthority(t *testing.T) {
    state := setupAppTestState(t)
    code := runApp(state, []string{"memory", "list", "--authority", "bogus"})
    if code != 2 { t.Fatalf("bogus authority must exit 2, got %d", code) }
}
func TestMemoryForgetMissingID(t *testing.T) {
    state := setupAppTestState(t)
    code := runApp(state, []string{"memory", "forget", "mem_does_not_exist"})
    if code != 3 { t.Fatalf("missing id must exit 3, got %d", code) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app -run "TestMemoryRemember|TestMemoryRejects|TestMemoryForget" -count=1`
Expected: FAIL

- [ ] **Step 3: Implement `internal/app/memory.go`**

Each subcommand is a function `func(ctx, args []string, store *Store, stdout, stderr io.Writer) int`:
- `memory list` — parse flags (--scope, --authority, --status, --limit, --json); call `store.List`; render ASCII table or JSON
- `memory show <id>` — `store.Get`; render full Memory struct
- `memory why <id>` — `store.Why`; render 6 sections (source, observed_at, scope, evidence, conflicts, recent_usage)
- `memory remember <claim>` — parse flags (--scope default project, --authority default explicit, --kind default fact, --valid-until, --source); build Memory; `store.Save`; print id
- `memory forget <id>` — parse --hard; `store.Forget`
- `memory supersede <old> <new>` — `store.Supersede`
- `memory approve <id>` — `store.Approve`
- `memory rebuild` — `store.Rebuild`
- `memory export` — parse flags (--scope, --status, --authority, --format default jsonl, --out); render JSON Lines or markdown; write to file or stdout

Exit codes per spec §5:
- 0 = 成功
- 1 = DB error
- 2 = 参数错误
- 3 = 找不到 id
- 4 = Authority 守门拒绝
- 5 = 冲突无法解决

- [ ] **Step 4: Register in `cmd/mengdie/main.go`**

Add `case "memory":` to the `run` switch, calling `runMemory(ctx, args[1:], stdout, stderr)`.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/app -run "TestMemoryRemember|TestMemoryRejects|TestMemoryForget" -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/app/memory.go internal/app/memory_test.go cmd/mengdie/main.go
git commit -m "feat(memory): add mengdie memory CLI with 9 subcommands"
```

---

## Task 10: Memory Trust Set 30 scenarios JSON

**Files:**
- Create: `evals/memory/trust-set-v1.json`

**Interfaces:**
- Consumes: spec §7 schema and 30-scenario distribution
- Produces: 30 scenarios

- [ ] **Step 1: Write the JSON file**

Create `evals/memory/trust-set-v1.json` with `schema_version: 1`, `suite_id: "memory-trust-set-v1"`, `description: "..."`, `scenarios: [...]`.

**15 explicit scenarios** (rotate through: new rule / duplicate correction / supersede / cross-project / forget / valid-until / hard-delete / export / list / show / missing-id / authority-reject / conflict / cross-branch):
```json
{"schema_version":1,"id":"user-confirms-test-command","category":"explicit","description":"用户显式确认测试入口","setup":{"seed_memories":[]},"actions":[{"type":"remember_user","claim":"用 go test ./... 作为测试入口"}],"expected":{"memory_present":true,"claim_match":"用 go test ./... 作为测试入口","authority":"explicit","status":"active","evidence_score_gte":0.5,"recallable":true,"forbid_duplicate":true,"forbid_old_status_change":false}}
```
(repeat for 15 explicit scenarios with varied claims about go test, lint config, commit message format, doc language, secret handling, AGENTS.md style, etc.)

**5 repository scenarios** (file source_type):
```json
{"schema_version":1,"id":"repo-go-mod-go-version","category":"repository","setup":{"seed_memories":[]},"actions":[{"type":"save_repository_fact","claim":"go.mod 声明 go 1.26.6","source":"go.mod:3"}],"expected":{"authority":"repository","status":"active","source_type":"file"}}
```
(repeat for: go.mod version, .golangci.yml rule, CI workflow OS matrix, AGENTS.md rule, git commit fact)

**5 verified scenarios** (command_result source_type):
```json
{"schema_version":1,"id":"verified-go-test-passes","category":"verified","setup":{"seed_memories":[]},"actions":[{"type":"save_verified_fact","claim":"go test ./... 退出码 0","source":"go test ./...:exit=0"}],"expected":{"authority":"verified","status":"active","source_type":"command_result"}}
```
(repeat for: go test pass, go vet pass, golangci-lint 0 issue, govulncheck no vuln, 4-target build pass)

**5 inferred scenarios** (agent_message source_type, must be `proposed`):
```json
{"schema_version":1,"id":"inferred-from-successful-run","category":"inferred","setup":{"seed_memories":[]},"actions":[{"type":"propose_memory","claim":"项目用 go 1.26.6"}],"expected":{"status":"proposed","authority":"inferred","forbid_active_before_approve":true}}
```
(repeat for: project structure inference, file pattern observation, naming convention, tool preference)

- [ ] **Step 2: Validate JSON parses**

Run: `python -c "import json; json.load(open('evals/memory/trust-set-v1.json'))"` or `go run ./cmd/mengdie-eval -h` (won't be needed) — just `jq` if available, else use the Go loader
Expected: parses without error; 30 entries; 15/5/5/5 distribution

- [ ] **Step 3: Commit**

```bash
git add evals/memory/trust-set-v1.json
git commit -m "test(memory): add trust-set-v1 with 30 explicit/repo/verified/inferred scenarios"
```

---

## Task 11: Trust Set runner

**Files:**
- Create: `internal/memory/trustset/runner.go`
- Test: `internal/memory/trustset/runner_test.go`

**Interfaces:**
- Consumes: `evals/memory/trust-set-v1.json`, `Store`, `Retriever`; spec §7 metrics
- Produces: `Result` with precision@5, false_recall_rate, source_traceability, authority_fidelity, why_completeness

- [ ] **Step 1: Write the failing runner test**

```go
func TestRunnerProducesAllMetrics(t *testing.T) {
    store := setupMemoryStoreWithSeeds(t)
    retriever := memory.NewRetriever(memory.OpenMemory(store))
    scenarios := loadScenariosForTest(t, 5)  // use 5 subset for speed
    r := trustset.Run(context.Background(), memory.OpenMemory(store), retriever, scenarios, "")
    if r.PrecisionAt5 < 0 || r.PrecisionAt5 > 1 { t.Fatalf("precision out of range: %v", r.PrecisionAt5) }
    if r.AuthorityFidelity != 1.0 { t.Fatalf("authority fidelity must be 1.0, got %v", r.AuthorityFidelity) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/memory/trustset -count=1`
Expected: FAIL

- [ ] **Step 3: Implement loader + runner**

In `internal/memory/trustset/runner.go`:
- `type Scenario struct { SchemaVersion int; ID, Category, Description string; Setup struct{ SeedMemories []Memory }; Actions []Action; Expected Expected }`
- `type Action struct { Type, Claim, Source string }` (Type ∈ {remember_user, save_repository_fact, save_verified_fact, propose_memory, approve, supersede, forget})
- `type Expected struct { MemoryPresent bool; ClaimMatch, Authority, Status, SourceType string; EvidenceScoreGte float64; Recallable, ForbidDuplicate, ForbidActiveBeforeApprove, ForbidOldStatusChange *bool }`
- `LoadScenarios(path string) ([]Scenario, error)` — read JSON, validate 30 entries
- `Run(ctx, store, retriever, scenarios, outPath string) Result`:
  1. For each scenario: setup → run actions → query retrieval with scenario's `query` claim → compute per-scenario pass/fail
  2. Aggregate metrics
  3. Write JSON evidence to outPath if non-empty
  4. Return Result

Per-scenario verification:
- `expected.MemoryPresent`: the last remember_user action's claim exists in store
- `expected.ClaimMatch`: the memory's claim equals expected.ClaimMatch
- `expected.Authority`: matches
- `expected.Status`: matches
- `expected.SourceType`: matches memory.Source.Type
- `expected.EvidenceScoreGte`: matches if non-zero
- `expected.Recallable`: tier3 returns this memory for `query = scenario.claim` query
- `expected.ForbidDuplicate`: only 1 memory with this claim exists
- `expected.ForbidActiveBeforeApprove`: status != 'active' unless `approve` action was called
- `expected.ForbidOldStatusChange`: superseded memory's status changed to 'superseded'

Metrics aggregation:
- `PrecisionAt5 = scenarios_with_ground_truth_in_top5 / total_recallable_scenarios`
- `FalseRecallRate = scenarios_with_wrong_authority_or_status_in_top5 / total_recallable_scenarios`
- `SourceTraceability = recallable_scenarios_with_at_least_one_evidence / total_recallable_scenarios`
- `AuthorityFidelity = scenarios_passing_authority_check / total_scenarios`
- `WhyCompleteness = scenarios_with_complete_why_report / total_scenarios`

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/memory/trustset -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/memory/trustset/runner.go internal/memory/trustset/runner_test.go
git commit -m "feat(memory): add Trust Set runner with 5-metric evaluation"
```

---

## Task 12: `liveprovider` end-to-end test

**Files:**
- Create: `internal/memory/live_provider_test.go` (build tag `//go:build liveprovider`)
- Create: `evidence/memory-live-trace-template.json` (just a comment, not a real file)

**Interfaces:**
- Consumes: real OpenAI-compatible Provider, real `state.db`; spec §8.1 live provider gate
- Produces: redaction-checked evidence file

- [ ] **Step 1: Write the failing test**

```go
//go:build liveprovider

func TestLiveProviderMemoryEndToEnd(t *testing.T) {
    if os.Getenv("MENGDIE_LIVE_SMOKE") != "1" { t.Skip("set MENGDIE_LIVE_SMOKE=1") }
    baseURL := requiredEnv(t, "MENGDIE_LIVE_BASE_URL")
    apiKey := requiredEnv(t, "MENGDIE_LIVE_API_KEY")
    model := requiredEnv(t, "MENGDIE_LIVE_MODEL")
    dataDir := t.TempDir()
    store := openMemoryStore(t, dataDir)
    // remember
    s := memory.OpenMemory(store)
    mem, err := s.SaveUserMemory(context.Background(), memory.Memory{
        Claim: "M3 live test: 项目使用 go test ./...", Authority: memory.AuthorityExplicit,
        Scope: memory.Scope{Kind:"project",Value:"live-test"},
        Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref:"live:user"},
    })
    if err != nil { t.Fatal(err) }
    if mem.ID == "" { t.Fatal("id empty") }
    // recall
    r := memory.NewRetriever(s)
    hits, _ := r.Tier3AtomicRecall(context.Background(), "M3 live test", 3, memory.Scope{Kind:"project",Value:"live-test"})
    if len(hits) == 0 { t.Fatal("live recall empty") }
    // redaction
    summary := struct{ ID, Claim string }{mem.ID, mem.Claim}
    raw, _ := json.Marshal(summary)
    if bytes.Contains(raw, []byte(apiKey)) { t.Fatal("evidence leaked api key") }
    // write evidence
    if err := os.WriteFile(filepath.Join("evidence", fmt.Sprintf("memory-live-%s.json", runtime.GOOS)), raw, 0o600); err != nil {
        t.Fatal(err)
    }
}
```

- [ ] **Step 2: Run test to verify it skips without env**

Run: `go test -tags=liveprovider -run TestLiveProviderMemoryEndToEnd ./internal/memory -count=1`
Expected: SKIP (env var missing)

- [ ] **Step 3: Run test with env (manual)**

Local validation only (since macOS scheduler is out of this session's reach):
```bash
MENGDIE_LIVE_SMOKE=1 MENGDIE_LIVE_BASE_URL=https://api.deepseek.com \
MENGDIE_LIVE_API_KEY=$MENGDIE_LIVE_API_KEY MENGDIE_LIVE_MODEL=deepseek-chat \
go test -tags=liveprovider -run TestLiveProviderMemoryEndToEnd ./internal/memory -count=1
```
Expected: PASS, `evidence/memory-live-{os}.json` written

- [ ] **Step 4: Commit**

```bash
git add internal/memory/live_provider_test.go
git commit -m "test(memory): add liveprovider end-to-end with redaction check"
```

---

## Task 13: CI integration

**Files:**
- Modify: `.github/workflows/ci.yml`
- Create: `.github/workflows/memory-live-provider.yml`

**Interfaces:**
- Consumes: spec §8.1 CI gates
- Produces: PR + scheduled macOS/Windows runs

- [ ] **Step 1: Add memory steps to `ci.yml`**

In `.github/workflows/ci.yml`, inside the `quality` job, after the chaos step:
```yaml
- name: 运行 Memory Trust Set
  run: go test -race ./internal/memory/... -run TestMemoryTrustSetV1 -count=1

- name: 上传 Memory 证据
  if: always()
  uses: actions/upload-artifact@v4
  with:
    name: memory-trust-v1-${{ github.run_id }}
    path: evidence/memory-trust-v1.json
```

- [ ] **Step 2: Create `memory-live-provider.yml`**

Mirror `chaos-live-provider.yml` but run `go test -tags=liveprovider -run TestLiveProviderMemoryEndToEnd ./internal/memory/... -count=1` and upload `evidence/memory-live-*.json`.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml .github/workflows/memory-live-provider.yml
git commit -m "ci(memory): add Trust Set step + live provider schedule"
```

---

## Task 14: Documentation + README + Beads close

**Files:**
- Create: `docs/development/phase-3-slice-01/IMPLEMENTATION_REPORT.md`
- Modify: `README.md`
- (Beads: `bd close mengdie-n47` after PR review)

**Interfaces:**
- Consumes: spec §10, this plan
- Produces: implementation report + README updates

- [ ] **Step 1: Write implementation report**

Create `docs/development/phase-3-slice-01/IMPLEMENTATION_REPORT.md` mirroring the structure of P2-08B report:
- 交付范围
- 故障/退路语义
- 验证（go fmt / vet / test -race / lint / govulncheck / 4-target build / Trust Set 5 指标）
- v0.1 follow-up（自动候选提取 / 嵌入向量 / Reflect 整合等）
- 红线检查

- [ ] **Step 2: Update README**

In `README.md`, change:
```markdown
- [ ] M3：可审计的可信记忆
```
to:
```markdown
- [x] 第三阶段 Slice 01：可信记忆 schema + FTS5 + 显式 CLI + Agent 集成（[设计稿](./docs/superpowers/specs/2026-08-24-m3-slice-01-trusted-memory-design.md)、[实施报告](./docs/development/phase-3-slice-01/IMPLEMENTATION_REPORT.md)）
- [ ] M3 Slice 02：任务结束候选提取 + MemoryExtractor（待办）
```

- [ ] **Step 3: Commit**

```bash
git add docs/development/phase-3-slice-01/IMPLEMENTATION_REPORT.md README.md
git commit -m "docs(memory): M3 Slice 01 implementation report + README"
```

---

## Execution Order & Gate

| Order | Task | Output |
|---|---|---|
| 1 | Task 1: 008_memory.sql migration | `internal/session/migrations/008_memory.sql` + test pass |
| 2 | Task 2: Memory types | `internal/memory/memory.go` + test pass |
| 3 | Task 3: Store Save (Authority routing) | `internal/memory/store.go` + 2 test pass |
| 4 | Task 4: Store List/Get/Why | `internal/memory/store.go` + 2 test pass |
| 5 | Task 5: Store mutation + evidence_score | `internal/memory/store.go` + 4 test pass |
| 6 | Task 6: Retriever 3-level | `internal/memory/retrieve.go` + 3 test pass |
| 7 | Task 7: Agent integration | `internal/agent/runtime.go` + integration test pass |
| 8 | Task 8: memory_recall tool | `internal/tools/memory_recall.go` + test pass |
| 9 | Task 9: 9 CLI subcommands | `internal/app/memory.go` + 3 test pass |
| 10 | Task 10: Trust Set 30 scenarios | `evals/memory/trust-set-v1.json` |
| 11 | Task 11: Trust Set runner | `internal/memory/trustset/runner.go` + test pass |
| 12 | Task 12: liveprovider test | `internal/memory/live_provider_test.go` (skip without env) |
| 13 | Task 13: CI integration | `ci.yml` + `memory-live-provider.yml` |
| 14 | Task 14: Docs + README | `IMPLEMENTATION_REPORT.md` + README |

After all 14 tasks, run final quality gates:
```bash
gofmt -l .                       # must be empty
go vet ./...
go test -race ./...             # all must pass (except known Windows shell test)
golangci-lint run ./...         # 0 issues
govulncheck@v1.1.4 ./...        # no vulns
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./cmd/...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./cmd/...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/...
go run ./cmd/mengdie-eval chaos --manifest evals/chaos/all.json --rounds 1   # M2 still green
go test -race ./internal/memory -run TestMemoryTrustSetV1 -count=1  # 5 metrics all green
```

Then open a PR with all 14 commits; CI must pass before user review per `ready-for-review-pr-beads` memory.
