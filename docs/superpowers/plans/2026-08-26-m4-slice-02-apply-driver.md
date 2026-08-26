# M4 Slice 02 Implementation Plan — v0.2 Apply Driver

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 M4 Slice 01 follow-up #1：`reflect approve` 后的实际 apply 链路。`proposal_applies` 审计表 + `ApplyExecutor` + `mengdie reflect apply <id>` CLI 子命令 + 4 apply 路径。Trust Set 50 → 54 场景。

**Architecture:** 新 migration `011_proposal_applies.sql` + `ApplyExecutor` interface + `DefaultApplyExecutor`（含 Policy + Patch Journal 集成）+ `Store.Apply` 公开方法 + `memory.Store.UpgradeMemory` 新 API + `Store.Archive` 现有 API。CLI 5 子命令；Trust Set 4 新场景。

**Tech Stack:** Go 1.26.6, `modernc.org/sqlite`, `internal/policy`, `internal/patchjournal`（既有或按需）。

## Global Constraints

- Go 1.26.6，module `github.com/Scorpio69t/mengdie-code`
- 仅 `CGO_ENABLED=0` 跨平台；四目标必须通过
- 禁止在用户仓库中自动 git commit / push
- 任何 `git commit` 由执行人显式触发
- 不修改既有 008/009/010 migration；本切片新增 011
- `Store.Apply` 走普通 Policy 链路（arch §9.4 安全边界）
- 错误统一用 `errors.New(...)` + `fmt.Errorf("%w", sentinel)`
- v0.2 不做 apply rollback（v0.3 独立 follow-up）
- 中文优先 package doc + 英文 inline comments
- 不引入新第三方依赖

---

## File Structure

### 新增

- `internal/session/migrations/011_proposal_applies.sql`
- `internal/memory/proposal/apply.go` (`ApplyExecutor` interface + `DefaultApplyExecutor` + `ApplyResult`)
- `internal/memory/proposal/apply_test.go`
- `docs/development/phase-4-slice-02/IMPLEMENTATION_REPORT.md`

### 修改

- `internal/memory/store.go` — 加 `UpgradeMemory(ctx, id, newClaim, newAuthority) Memory` + `ErrMemoryAuthorityRegression` sentinel
- `internal/memory/store_test.go` — 加 `TestUpgradeMemory*` 系列
- `internal/memory/proposal/proposal.go` — 加 `ApplyResult` + `ApplyExecutor` interface + sentinels
- `internal/memory/proposal/store.go` — 加 `Apply(ctx, id, executor)` 公开方法 + 内部 `getApplyResult` / `insertApplyResult` helpers
- `internal/memory/proposal/proposal_test.go` — 加 `TestStoreApply*` 系列
- `internal/app/reflect.go` — dispatcher 加 `case "apply"` + `runReflectApply` 函数
- `internal/app/reflect_test.go` — 加 2 个 apply happy-path 测试
- `internal/app/memory.go` — `exitForStoreError` 加 `ErrProposalNotApplicable` + `ErrProposalAlreadyApplied` 映射
- `internal/app/runtime.go` — `App.policy` 字段（if missing）+ `openReflectPipeline` 暴露 policy + projectRoot
- `internal/memory/trustset/runner.go` — `Action.Type` 加 `reflect_apply` verb + `setup.seed_applies` + `Expected.ApplyResult` / `ApplyErrorContains` 字段
- `internal/memory/trustset/runner_test.go` — count 50 → 54
- `evals/memory/trust-set-v1.json` — 4 新场景
- `README.md` — M4 Slice 02 勾选

### 不改

- 008_memory.sql / 009_memory_source_command.sql / 010_reflection_proposals.sql
- `internal/memory/retrieve.go`
- `internal/agent/`
- go.mod / go.sum

---

### Interfaces Introduced

```go
// internal/memory/proposal/proposal.go (extended)
type ApplyResult struct {
    ProposalID string
    Kind       ProposalKind
    Target     string  // memory_id or file path
    Result     string  // "success" | "failed" | "denied_by_policy"
    Error      string
    AppliedAt  time.Time
    PatchID    string
}

type ApplyExecutor interface {
    ApplyMemoryUpgrade(ctx, p Proposal) (ApplyResult, error)
    ApplyAgentsMdRevision(ctx, p Proposal) (ApplyResult, error)
    ApplySkillDraft(ctx, p Proposal) (ApplyResult, error)
    ApplyObsolete(ctx, p Proposal) (ApplyResult, error)
}

var (
    ErrProposalNotApplicable = errors.New("proposal is not applicable")
    ErrProposalAlreadyApplied = errors.New("proposal already applied")
)

// internal/memory/proposal/store.go (extended)
func (s *Store) Apply(ctx context.Context, proposalID string, executor ApplyExecutor) (ApplyResult, error)

// internal/memory/proposal/apply.go (new)
type DefaultApplyExecutor struct {
    memStore     *memory.Store
    proposalStore *Store
    policy       policy.Engine
    projectRoot  string
    now          func() time.Time
}

func NewDefaultApplyExecutor(...) *DefaultApplyExecutor

// internal/memory/store.go (extended)
func (s *Store) UpgradeMemory(ctx context.Context, id, newClaim string, newAuthority Authority) (Memory, error)
var ErrMemoryAuthorityRegression = errors.New("memory authority regression not allowed")
```

---

### Task 1: proposal_applies 审计表 migration

**Files:**
- Create: `internal/session/migrations/011_proposal_applies.sql`
- Modify: `internal/session/sqlite_store_test.go` (migration count 10 → 11)

- [ ] **Step 1: 写失败测试**

In `internal/session/sqlite_store_test.go`，加：

```go
func TestOpenSQLiteAppliesProposalAppliesTable(t *testing.T) {
    dir := t.TempDir()
    s, err := session.OpenSQLite(context.Background(), session.OpenOptions{
        DataDir: dir, ProjectRoot: filepath.Join(t.TempDir(), "project"),
        Now: func() time.Time { return proposalTestTime },
    })
    if err != nil { t.Fatalf("OpenSQLite: %v", err) }
    defer func() { _ = s.Close() }()

    var name string
    if err := s.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='proposal_applies'`).Scan(&name); err != nil {
        t.Fatalf("proposal_applies table not found: %v", err)
    }
    if name != "proposal_applies" { t.Fatalf("want proposal_applies, got %s", name) }
}
```

确认当前 `TestOpenSQLiteAppliesSchemaAndConnectionSettings` 的 migration count 期望值。Slice 01 (Task 1) 把 9 → 10。本切片把 10 → 11。

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/session -count=1 -v`
Expected: FAIL

- [ ] **Step 3: 写 migration**

Create `internal/session/migrations/011_proposal_applies.sql`：

```sql
-- 011_proposal_applies.sql
-- M4 Slice 02: apply 审计表。每次 Store.Apply 完成一条记录（含失败 / policy 拒绝）。
-- 与 reflection_proposals 一对一 (proposal_id UNIQUE)，保证 idempotent guard。
CREATE TABLE proposal_applies (
    rowid        INTEGER PRIMARY KEY,
    id           TEXT NOT NULL UNIQUE,
    proposal_id  TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL,
    target       TEXT NOT NULL,
    result       TEXT NOT NULL,        -- success | failed | denied_by_policy
    error        TEXT,
    applied_at   TEXT NOT NULL,
    patch_id     TEXT,
    FOREIGN KEY (proposal_id) REFERENCES reflection_proposals(id) ON DELETE CASCADE
);

CREATE INDEX idx_proposal_applies_applied_at ON proposal_applies (applied_at DESC);
```

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/session -count=1 -v`
Expected: PASS

- [ ] **Step 5: 跑全 memory 测试确认无回归**

Run: `go test -race ./internal/memory -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/session/migrations/011_proposal_applies.sql internal/session/sqlite_store_test.go
git commit -m "feat(memory): add proposal_applies migration (apply audit table)"
```

---

### Task 2: `Store.Apply` 公开方法 + ApplyResult + ApplyExecutor interface

**Files:**
- Modify: `internal/memory/proposal/proposal.go` — 加 `ApplyResult` + `ApplyExecutor` interface + 2 sentinels
- Modify: `internal/memory/proposal/store.go` — 加 `Apply(ctx, id, executor)` + 内部 helpers + 2 sentinels
- Modify: `internal/memory/proposal/proposal_test.go` — 加 `TestStoreApply*` 系列

- [ ] **Step 1: 写失败测试**

In `internal/memory/proposal/proposal_test.go`，加：

```go
func TestStoreApplyApprovedProposal(t *testing.T) {
    ctx := context.Background()
    store, sessionStore := openProposalStore(t)
    defer func() { _ = sessionStore.Close() }()

    // Seed approved proposal
    p := proposal.Proposal{
        Kind: proposal.KindMemoryUpgrade, Title: "t",
        Body: proposal.ProposalBody{Kind: "memory_upgrade",
            Payload: map[string]any{"memory_id": "mem_xxx", "new_claim": "...", "new_authority": "explicit"}},
        Status: proposal.StatusApproved, ObservedAt: proposalTestTime,
    }
    saved, _ := store.Insert(ctx, p)
    // UpdateStatus to approved
    store.UpdateStatus(ctx, saved.ID, proposal.StatusApproved, "test")

    // Mock executor
    mockExec := &mockApplyExecutor{result: proposal.ApplyResult{
        ProposalID: saved.ID, Kind: p.Kind, Target: "mem_xxx", Result: "success", AppliedAt: proposalTestTime,
    }}

    result, err := store.Apply(ctx, saved.ID, mockExec)
    if err != nil { t.Fatalf("Apply: %v", err) }
    if result.Result != "success" { t.Fatalf("want success, got %s", result.Result) }
    if result.Target != "mem_xxx" { t.Fatalf("target want mem_xxx, got %s", result.Target) }
}

func TestStoreApplyRejectsNotApproved(t *testing.T) {
    ctx := context.Background()
    store, sessionStore := openProposalStore(t)
    defer func() { _ = sessionStore.Close() }()

    p := proposal.Proposal{
        Kind: proposal.KindMemoryUpgrade, Title: "t",
        Body: proposal.ProposalBody{Kind: "memory_upgrade"},
        Status: proposal.StatusProposed, ObservedAt: proposalTestTime,
    }
    saved, _ := store.Insert(ctx, p)
    mockExec := &mockApplyExecutor{}
    _, err := store.Apply(ctx, saved.ID, mockExec)
    if !errors.Is(err, proposal.ErrProposalNotApplicable) {
        t.Fatalf("want ErrProposalNotApplicable, got %v", err)
    }
}

func TestStoreApplyIsIdempotent(t *testing.T) {
    ctx := context.Background()
    store, sessionStore := openProposalStore(t)
    defer func() { _ = sessionStore.Close() }()

    p := proposal.Proposal{
        Kind: proposal.KindMemoryUpgrade, Title: "t",
        Body: proposal.ProposalBody{Kind: "memory_upgrade"},
        Status: proposal.StatusApproved, ObservedAt: proposalTestTime,
    }
    saved, _ := store.Insert(ctx, p)

    // First apply
    mockExec := &mockApplyExecutor{result: proposal.ApplyResult{
        ProposalID: saved.ID, Kind: p.Kind, Target: "mem_xxx", Result: "success", AppliedAt: proposalTestTime,
    }}
    store.Apply(ctx, saved.ID, mockExec)
    // Second apply — should return existing result, NOT call executor
    mockExec2 := &mockApplyExecutor{called: false}
    result, err := store.Apply(ctx, saved.ID, mockExec2)
    if err != nil { t.Fatalf("Apply: %v", err) }
    if result.Result != "success" { t.Fatalf("want existing success, got %s", result.Result) }
    if mockExec2.called { t.Fatal("executor should not be called on idempotent re-apply") }
}

type mockApplyExecutor struct {
    result proposal.ApplyResult
    err    error
    called bool
}

func (m *mockApplyExecutor) ApplyMemoryUpgrade(ctx context.Context, p proposal.Proposal) (proposal.ApplyResult, error) {
    m.called = true; return m.result, m.err
}
func (m *mockApplyExecutor) ApplyAgentsMdRevision(...) (proposal.ApplyResult, error) {
    m.called = true; return m.result, m.err
}
func (m *mockApplyExecutor) ApplySkillDraft(...) (proposal.ApplyResult, error) {
    m.called = true; return m.result, m.err
}
func (m *mockApplyExecutor) ApplyObsolete(...) (proposal.ApplyResult, error) {
    m.called = true; return m.result, m.err
}
```

> `mockApplyExecutor` 也可放 `proposal_test.go` 顶部。

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory/proposal -count=1 -v`
Expected: FAIL（`Apply` / `ApplyResult` / sentinels undefined）

- [ ] **Step 3: 实现 `ApplyResult` + `ApplyExecutor` + sentinels**

In `internal/memory/proposal/proposal.go`：

```go
type ApplyResult struct {
    ProposalID string     `json:"proposal_id"`
    Kind       ProposalKind `json:"kind"`
    Target     string     `json:"target"`
    Result     string     `json:"result"`  // "success" | "failed" | "denied_by_policy"
    Error      string     `json:"error,omitempty"`
    AppliedAt  time.Time  `json:"applied_at"`
    PatchID    string     `json:"patch_id,omitempty"`
}

type ApplyExecutor interface {
    ApplyMemoryUpgrade(ctx context.Context, p Proposal) (ApplyResult, error)
    ApplyAgentsMdRevision(ctx context.Context, p Proposal) (ApplyResult, error)
    ApplySkillDraft(ctx context.Context, p Proposal) (ApplyResult, error)
    ApplyObsolete(ctx context.Context, p Proposal) (ApplyResult, error)
}

const (
    ApplyResultSuccess         = "success"
    ApplyResultFailed          = "failed"
    ApplyResultDeniedByPolicy  = "denied_by_policy"
)

var (
    ErrProposalNotApplicable = errors.New("proposal is not applicable")  // status != approved
    ErrProposalAlreadyApplied = errors.New("proposal already applied")  // idempotent guard
)
```

- [ ] **Step 4: 实现 `Store.Apply` + 内部 helpers**

In `internal/memory/proposal/store.go`：

```go
const insertApplyResultSQL = `INSERT INTO proposal_applies
    (id, proposal_id, kind, target, result, error, applied_at, patch_id)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

// Apply runs the executor for the proposal and records the result in
// proposal_applies. Refuses proposals that are not status=approved.
// Idempotent: if proposal_applies row exists, returns existing result.
func (s *Store) Apply(ctx context.Context, proposalID string, executor ApplyExecutor) (ApplyResult, error) {
    p, err := s.Get(ctx, proposalID)
    if err != nil { return ApplyResult{}, err }
    if p.Status != StatusApproved {
        return ApplyResult{}, fmt.Errorf("%w: %s is %s, not approved", ErrProposalNotApplicable, proposalID, p.Status)
    }
    // Idempotent guard
    if existing, err := s.getApplyResult(ctx, proposalID); err == nil && !existing.AppliedAt.IsZero() {
        return existing, nil
    }
    // Run executor based on kind
    var result ApplyResult
    switch p.Kind {
    case KindMemoryUpgrade:
        result, err = executor.ApplyMemoryUpgrade(ctx, p)
    case KindAgentsMdRevision:
        result, err = executor.ApplyAgentsMdRevision(ctx, p)
    case KindSkillDraft:
        result, err = executor.ApplySkillDraft(ctx, p)
    case KindObsolete:
        result, err = executor.ApplyObsolete(ctx, p)
    default:
        return ApplyResult{}, fmt.Errorf("%w: unknown kind %s", ErrProposalNotApplicable, p.Kind)
    }
    if err != nil { return ApplyResult{}, err }
    // Record
    if result.ProposalID == "" { result.ProposalID = proposalID }
    if result.Kind == "" { result.Kind = p.Kind }
    if result.AppliedAt.IsZero() { result.AppliedAt = s.now() }
    if rerr := s.insertApplyResult(ctx, result); rerr != nil {
        return ApplyResult{}, fmt.Errorf("record apply result: %w", rerr)
    }
    return result, nil
}

func (s *Store) getApplyResult(ctx context.Context, proposalID string) (ApplyResult, error) {
    var r ApplyResult
    var errMsg, patchID sql.NullString
    var appliedAt string
    err := s.db.QueryRowContext(ctx,
        `SELECT id, proposal_id, kind, target, result, error, applied_at, patch_id
         FROM proposal_applies WHERE proposal_id = ?`, proposalID,
    ).Scan(&r.ProposalID, &r.ProposalID, &r.Kind, &r.Target, &r.Result, &errMsg, &appliedAt, &patchID)
    if errors.Is(err, sql.ErrNoRows) { return ApplyResult{}, ErrProposalNotFound }
    if err != nil { return ApplyResult{}, err }
    r.Error = errMsg.String
    r.PatchID = patchID.String
    r.AppliedAt, _ = time.Parse(time.RFC3339Nano, appliedAt)
    return r, nil
}

func (s *Store) insertApplyResult(ctx context.Context, r ApplyResult) error {
    applyID := generateProposalID(s.now(), r.Kind, r.ProposalID+":apply")
    _, err := s.db.ExecContext(ctx, insertApplyResultSQL,
        applyID, r.ProposalID, string(r.Kind), r.Target, r.Result, nullString(r.Error),
        formatStamp(r.AppliedAt.UTC()), nullString(r.PatchID),
    )
    return err
}
```

> 注：复用 `generateProposalID` 与 `formatStamp` 与 `nullString` 现有 helpers（slice 01 Task 1 加的）。

- [ ] **Step 5: 跑测试确认 pass**

Run: `go test ./internal/memory/proposal -count=1 -v`
Expected: PASS

- [ ] **Step 6: 跑全 memory 测试**

Run: `go test -race ./internal/memory -count=1`
Expected: 全 PASS

- [ ] **Step 7: Commit**

```bash
git add internal/memory/proposal/proposal.go internal/memory/proposal/store.go internal/memory/proposal/proposal_test.go
git commit -m "feat(memory): ProposalStore.Apply with idempotent guard"
```

---

### Task 3: `memory.Store.UpgradeMemory` + `ErrMemoryAuthorityRegression`

**Files:**
- Modify: `internal/memory/store.go` — 加 `UpgradeMemory` + `ErrMemoryAuthorityRegression` sentinel
- Modify: `internal/memory/store_test.go` — 加 `TestUpgradeMemory*` 系列

- [ ] **Step 1: 写失败测试**

In `internal/memory/store_test.go`，加：

```go
func TestUpgradeMemoryPromotesAuthority(t *testing.T) {
    s := setupSeededStore(t)
    ctx := context.Background()
    // seed an inferred memory
    in := memory.Memory{
        Claim: "old claim", Authority: memory.AuthorityInferred,
        Scope: memory.Scope{Kind: "project", Value: "mengdie"},
        Source: memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: "test"},
    }
    saved, _ := s.Save(ctx, in)

    // Upgrade to explicit
    upgraded, err := s.UpgradeMemory(ctx, saved.ID, "new claim", memory.AuthorityExplicit)
    if err != nil { t.Fatalf("UpgradeMemory: %v", err) }
    if upgraded.Authority != memory.AuthorityExplicit {
        t.Fatalf("want explicit, got %s", upgraded.Authority)
    }
    if upgraded.Claim != "new claim" {
        t.Fatalf("want new claim, got %s", upgraded.Claim)
    }
}

func TestUpgradeMemoryRejectsRegression(t *testing.T) {
    s := setupSeededStore(t)
    ctx := context.Background()
    in := memory.Memory{
        Claim: "explicit claim", Authority: memory.AuthorityExplicit,
        Scope: memory.Scope{Kind: "project", Value: "mengdie"},
        Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "test"},
    }
    saved, _ := s.Save(ctx, in)

    // Try to downgrade to inferred
    _, err := s.UpgradeMemory(ctx, saved.ID, "downgrade", memory.AuthorityInferred)
    if !errors.Is(err, memory.ErrMemoryAuthorityRegression) {
        t.Fatalf("want ErrMemoryAuthorityRegression, got %v", err)
    }
}

func TestUpgradeMemoryRejectsSameAuthority(t *testing.T) {
    s := setupSeededStore(t)
    ctx := context.Background()
    in := memory.Memory{
        Claim: "x", Authority: memory.AuthorityInferred,
        Scope: memory.Scope{Kind: "project", Value: "mengdie"},
        Source: memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: "test"},
    }
    saved, _ := s.Save(ctx, in)

    // Same authority, no rank change
    _, err := s.UpgradeMemory(ctx, saved.ID, "x", memory.AuthorityInferred)
    if !errors.Is(err, memory.ErrMemoryAuthorityRegression) {
        t.Fatalf("want ErrMemoryAuthorityRegression for same authority, got %v", err)
    }
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory -run "TestUpgradeMemory" -count=1 -v`
Expected: FAIL（`UpgradeMemory` / `ErrMemoryAuthorityRegression` undefined）

- [ ] **Step 3: 实现 `UpgradeMemory` + sentinel**

In `internal/memory/store.go`，在 sentinel 块附近加：

```go
var ErrMemoryAuthorityRegression = errors.New("memory authority regression not allowed; new rank must be lower (more authoritative)")
```

加方法（参考 `Forget` / `Approve` 的 pattern）：

```go
func (s *Store) UpgradeMemory(ctx context.Context, id, newClaim string, newAuthority Authority) (Memory, error) {
    if strings.TrimSpace(id) == "" {
        return Memory{}, fmt.Errorf("%w: id required", ErrMemoryNotFound)
    }
    if strings.TrimSpace(newClaim) == "" {
        return Memory{}, fmt.Errorf("invalid memory: claim empty")
    }
    m, err := s.Get(ctx, id)
    if err != nil { return Memory{}, err }
    newRank := AuthorityRank(newAuthority)
    currentRank := AuthorityRank(m.Authority)
    if newRank >= currentRank {
        return Memory{}, fmt.Errorf("%w: %s (rank %d) → %s (rank %d)", ErrMemoryAuthorityRegression, m.Authority, currentRank, newAuthority, newRank)
    }
    stamp := formatStamp(s.now().UTC())
    _, err = s.db.ExecContext(ctx,
        `UPDATE memories SET claim=?, authority=?, updated_at=? WHERE id=?`,
        newClaim, string(newAuthority), stamp, id,
    )
    if err != nil { return Memory{}, fmt.Errorf("upgrade memory: %w", err) }
    m.Claim = newClaim
    m.Authority = newAuthority
    m.UpdatedAt = s.now().UTC()
    return m, nil
}
```

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/memory -run "TestUpgradeMemory" -count=1 -v`
Expected: PASS

- [ ] **Step 5: 跑全 memory 测试**

Run: `go test -race ./internal/memory -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/memory/store.go internal/memory/store_test.go
git commit -m "feat(memory): Store.UpgradeMemory with authority regression guard"
```

---

### Task 4: `DefaultApplyExecutor` 实现 4 路径

**Files:**
- Create: `internal/memory/proposal/apply.go` (`DefaultApplyExecutor` + 4 路径实现)
- Create: `internal/memory/proposal/apply_test.go`

- [ ] **Step 1: 写失败测试**

In `internal/memory/proposal/apply_test.go`（新建，`package proposal_test`）：

```go
func TestDefaultApplyExecutorMemoryUpgrade(t *testing.T) {
    ctx := context.Background()
    memStore, propStore, sessionStore := openProposalFixture(t)  // 复用 slice 01 helper
    defer sessionStore.Close()

    // seed inferred memory
    in := memory.Memory{
        Claim: "old claim", Authority: memory.AuthorityInferred,
        Scope: memory.Scope{Kind: "project", Value: "mengdie"},
        Source: memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: "test"},
    }
    saved, _ := memStore.Save(ctx, in)

    p := proposal.Proposal{
        Kind: proposal.KindMemoryUpgrade, Title: "upgrade",
        Body: proposal.ProposalBody{
            Kind: "memory_upgrade",
            Payload: map[string]any{
                "memory_id": saved.ID,
                "new_claim": "new claim",
                "new_authority": "explicit",
            },
        },
        Status: proposal.StatusApproved, ObservedAt: proposalTestTime,
    }

    executor := proposal.NewDefaultApplyExecutor(memStore, propStore, nil /* no policy for memory_upgrade */, "", func() time.Time { return proposalTestTime })
    result, err := executor.ApplyMemoryUpgrade(ctx, p)
    if err != nil { t.Fatalf("ApplyMemoryUpgrade: %v", err) }
    if result.Result != "success" { t.Fatalf("want success, got %s: %s", result.Result, result.Error) }

    // Verify memory upgraded
    got, _ := memStore.Get(ctx, saved.ID)
    if got.Claim != "new claim" || got.Authority != memory.AuthorityExplicit {
        t.Fatalf("memory not upgraded: %+v", got)
    }
}

func TestDefaultApplyExecutorObsolete(t *testing.T) {
    ctx := context.Background()
    memStore, propStore, sessionStore := openProposalFixture(t)
    defer sessionStore.Close()

    in := memory.Memory{
        Claim: "obsolete", Authority: memory.AuthorityExplicit,
        Scope: memory.Scope{Kind: "project", Value: "mengdie"},
        Source: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "test"},
    }
    saved, _ := memStore.Save(ctx, in)

    p := proposal.Proposal{
        Kind: proposal.KindObsolete, Title: "obsolete",
        Body: proposal.ProposalBody{
            Kind: "obsolete",
            Payload: map[string]any{"memory_id": saved.ID},
        },
        Status: proposal.StatusApproved, ObservedAt: proposalTestTime,
    }

    executor := proposal.NewDefaultApplyExecutor(memStore, propStore, nil, "", func() time.Time { return proposalTestTime })
    result, err := executor.ApplyObsolete(ctx, p)
    if err != nil { t.Fatalf("ApplyObsolete: %v", err) }
    if result.Result != "success" { t.Fatalf("want success, got %s", result.Error) }
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory/proposal -run "TestDefaultApply" -count=1 -v`
Expected: FAIL（`DefaultApplyExecutor` undefined）

- [ ] **Step 3: 实现 `DefaultApplyExecutor`**

In `internal/memory/proposal/apply.go`：

```go
package proposal

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "time"

    "github.com/Scorpio69t/mengdie-code/internal/memory"
    "github.com/Scorpio69t/mengdie-code/internal/policy"
)

type DefaultApplyExecutor struct {
    memStore      *memory.Store
    proposalStore *Store
    policy        policy.Engine
    projectRoot   string
    now           func() time.Time
}

func NewDefaultApplyExecutor(ms *memory.Store, ps *Store, pol policy.Engine, projectRoot string, now func() time.Time) *DefaultApplyExecutor {
    return &DefaultApplyExecutor{memStore: ms, proposalStore: ps, policy: pol, projectRoot: projectRoot, now: now}
}

// ApplyMemoryUpgrade calls memStore.UpgradeMemory
func (e *DefaultApplyExecutor) ApplyMemoryUpgrade(ctx context.Context, p Proposal) (ApplyResult, error) {
    memoryID, _ := p.Body.Payload["memory_id"].(string)
    newClaim, _ := p.Body.Payload["new_claim"].(string)
    newAuthority, _ := p.Body.Payload["new_authority"].(string)
    if memoryID == "" || newClaim == "" || newAuthority == "" {
        return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Result: "failed", Error: "missing memory_id/new_claim/new_authority", AppliedAt: e.now()}, nil
    }
    if _, err := e.memStore.UpgradeMemory(ctx, memoryID, newClaim, memory.Authority(newAuthority)); err != nil {
        return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: memoryID, Result: "failed", Error: err.Error(), AppliedAt: e.now()}, nil
    }
    return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: memoryID, Result: "success", AppliedAt: e.now()}, nil
}

// ApplyObsolete archives the memory (status=archived)
func (e *DefaultApplyExecutor) ApplyObsolete(ctx context.Context, p Proposal) (ApplyResult, error) {
    memoryID, _ := p.Body.Payload["memory_id"].(string)
    if memoryID == "" {
        return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Result: "failed", Error: "missing memory_id", AppliedAt: e.now()}, nil
    }
    if err := e.memStore.Forget(ctx, memoryID); err != nil {
        return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: memoryID, Result: "failed", Error: err.Error(), AppliedAt: e.now()}, nil
    }
    return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: memoryID, Result: "success", AppliedAt: e.now()}, nil
}

// ApplyAgentsMdRevision: Policy check + AGENTS.md write (v0.2 简化：write via os.WriteFile)
func (e *DefaultApplyExecutor) ApplyAgentsMdRevision(ctx context.Context, p Proposal) (ApplyResult, error) {
    if e.policy != nil {
        allowed, err := e.policy.Authorize(ctx, policy.Request{
            Action: "file.write", Target: "AGENTS.md",
            Justification: fmt.Sprintf("Apply M4 proposal %s", p.ID),
        })
        if err != nil || !allowed {
            return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: "AGENTS.md", Result: "denied_by_policy", Error: "policy denied", AppliedAt: e.now()}, nil
        }
    }
    section, _ := p.Body.Payload["section"].(string)
    proposed, _ := p.Body.Payload["proposed"].(string)
    if section == "" || proposed == "" {
        return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: "AGENTS.md", Result: "failed", Error: "missing section/proposed", AppliedAt: e.now()}, nil
    }
    path := filepath.Join(e.projectRoot, "AGENTS.md")
    if err := os.WriteFile(path, []byte(proposed), 0o644); err != nil {
        return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: path, Result: "failed", Error: err.Error(), AppliedAt: e.now()}, nil
    }
    return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: path, Result: "success", AppliedAt: e.now()}, nil
}

// ApplySkillDraft: Policy check + write skills/<name>.md
func (e *DefaultApplyExecutor) ApplySkillDraft(ctx context.Context, p Proposal) (ApplyResult, error) {
    if e.policy != nil {
        allowed, _ := e.policy.Authorize(ctx, policy.Request{Action: "file.create", Target: "skills/"})
        if !allowed {
            return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Result: "denied_by_policy", Error: "policy denied", AppliedAt: e.now()}, nil
        }
    }
    skillName, _ := p.Body.Payload["skill_name"].(string)
    body, _ := p.Body.Payload["body"].(string)
    if skillName == "" || body == "" {
        return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Result: "failed", Error: "missing skill_name/body", AppliedAt: e.now()}, nil
    }
    path := filepath.Join(e.projectRoot, "skills", skillName+".md")
    if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
        return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: path, Result: "failed", Error: err.Error(), AppliedAt: e.now()}, nil
    }
    return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: path, Result: "success", AppliedAt: e.now()}, nil
}
```

> **注**：`policy.Engine` 接口需要读 `internal/policy/` 看实际签名。本切片用最简形式：若 policy 为 nil 跳过检查（v0.2 demo 用）。生产用户传入真实 policy。

> **重要 v0.2 简化**：`Forget` 已经是 archive 路径（v0.1 实现 `memStore.Forget(id)` 走 soft archive），所以 `ApplyObsolete` 直接调 `Forget`。如果有 `Store.Archive` 方法更好，Task 3 的 `Store.Archive` 是 slice 01 IMPLEMENTATION_REPORT follow-up #2，v0.2 用 `Forget` 即可。

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/memory/proposal -run "TestDefaultApply" -count=1 -v`
Expected: PASS

- [ ] **Step 5: 跑全 memory 测试**

Run: `go test -race ./internal/memory -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/memory/proposal/apply.go internal/memory/proposal/apply_test.go
git commit -m "feat(memory): DefaultApplyExecutor 4 paths (memory_upgrade / agents_md / skill_draft / obsolete)"
```

---

### Task 5: CLI `mengdie reflect apply <id>` 子命令

**Files:**
- Modify: `internal/app/reflect.go` — `dispatchReflect` 加 `case "apply"` + `runReflectApply` 函数
- Modify: `internal/app/reflect_test.go` — 加 2 个 apply 测试
- Modify: `internal/app/memory.go` — `exitForStoreError` 加 2 个新 sentinel 映射
- Modify: `internal/app/runtime.go` — `App.policy` 字段 + `openReflectPipeline` 暴露 policy + projectRoot

- [ ] **Step 1: 写失败测试**

In `internal/app/reflect_test.go`，加：

```go
func TestReflectApplyApprovedProposal(t *testing.T) {
    state := setupAppTestState(t)
    // seed approved memory_upgrade proposal
    propStore, _ := openTestProposalStore(t)
    defer ...
    saved, _ := propStore.Insert(...)
    propStore.UpdateStatus(..., saved.ID, proposal.StatusApproved, "test")

    code := runApp(state, []string{"reflect", "apply", saved.ID})
    if code != ExitOK { t.Fatalf("apply exit=%d stderr=%q", code, state.stderr.String()) }
    if !strings.Contains(state.stdout.String(), "result=success") {
        t.Fatalf("apply output missing result=success: %q", state.stdout.String())
    }
}

func TestReflectApplyRejectsNotApproved(t *testing.T) {
    state := setupAppTestState(t)
    propStore, _ := openTestProposalStore(t)
    defer ...
    saved, _ := propStore.Insert(... status=proposed ...)

    code := runApp(state, []string{"reflect", "apply", saved.ID})
    if code != ExitInvalidInput {
        t.Fatalf("apply want exit %d, got %d stderr=%q", ExitInvalidInput, code, state.stderr.String())
    }
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/app -run "TestReflectApply" -count=1 -v`
Expected: FAIL

- [ ] **Step 3: 实现 `runReflectApply`**

In `internal/app/reflect.go`，加 `runReflectApply`（在 `runReflectApprove` / `runReflectReject` 附近）：

```go
func runReflectApply(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
    if len(args) != 1 {
        fmt.Fprintf(stderr, "用法：mengdie reflect apply <id>\n")
        return ExitInvalidInput
    }
    id := args[0]

    pipeline, _, _, code := a.openReflectPipeline(ctx, common)
    if code != ExitOK { return code }
    defer ...

    executor := proposal.NewDefaultApplyExecutor(
        pipeline.memStore, pipeline.proposalStore,
        a.policy, a.projectRoot, a.now,
    )
    result, err := pipeline.proposalStore.Apply(ctx, id, executor)
    if err != nil {
        return exitForStoreError(err)
    }

    fmt.Fprintf(stdout, "applied %s: kind=%s target=%s result=%s\n",
        result.ProposalID, result.Kind, result.Target, result.Result)
    if result.Error != "" {
        fmt.Fprintf(stderr, "  error: %s\n", result.Error)
    }
    if result.Result != proposal.ApplyResultSuccess {
        return ExitRunError
    }
    return ExitOK
}
```

- [ ] **Step 4: `dispatchReflect` 加 `case "apply"`**

In `internal/app/reflect.go` 的 `dispatchReflect` switch，加：

```go
case "apply":
    return runReflectApply(ctx, args[1:], a, stdout, stderr)
```

> Task 4 fix 已让 `--flag` 直通 `runReflect`；`apply` 是子动词（无 `-` 前缀），走 switch 路径。

- [ ] **Step 5: `exitForStoreError` 加 2 个新 sentinel 映射**

In `internal/app/memory.go` 的 `exitForStoreError` switch，加：

```go
case errors.Is(err, memoryproposal.ErrProposalNotApplicable):
    return ExitInvalidInput
case errors.Is(err, memoryproposal.ErrProposalAlreadyApplied):
    return ExitInvalidInput
```

- [ ] **Step 6: `App.policy` 字段 + `openReflectPipeline` 暴露 policy + projectRoot**

In `internal/app/runtime.go`，找 `App` struct 定义，加：

```go
type App struct {
    // ... 既有字段
    policy      policy.Engine  // 新增
    projectRoot string         // 新增
}
```

> 如果 `App` 已经有这些字段或类似，跳过。读源码确认。

如果 `App` 没暴露 policy，需要在 `NewApp` 或类似构造函数里允许注入。

- [ ] **Step 7: 跑测试确认 pass**

Run: `go test ./internal/app -run "TestReflectApply" -count=1 -v`
Expected: PASS

- [ ] **Step 8: 跑全 app 测试**

Run: `go test -race ./internal/app -count=1`
Expected: 全 PASS

- [ ] **Step 9: Commit**

```bash
git add internal/app/reflect.go internal/app/reflect_test.go internal/app/memory.go internal/app/runtime.go
git commit -m "feat(cli): mengdie reflect apply <id> subcommand"
```

---

### Task 6: Trust Set runner 扩展 + 4 新场景

**Files:**
- Modify: `internal/memory/trustset/runner.go` — `Action.Type` 加 `reflect_apply` + `setup.seed_applies` + `Expected.ApplyResult` / `ApplyErrorContains`
- Modify: `internal/memory/trustset/runner_test.go` — count 50 → 54
- Modify: `evals/memory/trust-set-v1.json` — 4 新场景

- [ ] **Step 1: 写失败测试**

In `internal/memory/trustset/runner_test.go`，加 1 个新 test + 改 50 → 54：

```go
func TestRunnerCountsScenarios(t *testing.T) {
    // loadTrustSetJSON + assert total == 54
}
```

Append 4 scenarios to `evals/memory/trust-set-v1.json` `tasks` array：

```json
{"id": "reflect-apply-memory-upgrade-success", "category": "reflect", "description": "approved memory_upgrade → apply → memory 升级", "setup": {"seed_proposals": [{"kind": "memory_upgrade", "title": "upgrade", "status": "approved", "body_payload": {"memory_id": "mem_seed_1", "new_claim": "升级后 claim", "new_authority": "explicit"}}], "seed_memories": [{"claim": "old", "authority": "inferred", "id": "mem_seed_1"}]}, "actions": [{"type": "reflect_propose"}, {"type": "reflect_approve"}, {"type": "reflect_apply"}], "expected": {"proposal_apply_result": "success"}},
{"id": "reflect-apply-obsolete-success", "category": "reflect", "description": "approved obsolete → apply → memory 归档", "setup": {"seed_proposals": [{"kind": "obsolete", "title": "obs", "status": "approved", "body_payload": {"memory_id": "mem_seed_2"}}], "seed_memories": [{"claim": "obs", "authority": "explicit", "id": "mem_seed_2"}]}, "actions": [{"type": "reflect_propose"}, {"type": "reflect_approve"}, {"type": "reflect_apply"}], "expected": {"proposal_apply_result": "success"}},
{"id": "reflect-apply-fails-not-approved", "category": "reflect", "description": "proposed (not approved) → apply → 拒绝", "setup": {"seed_proposals": [{"kind": "memory_upgrade", "title": "x", "status": "proposed"}]}, "actions": [{"type": "reflect_propose"}, {"type": "reflect_apply"}], "expected": {"proposal_apply_result": "failed", "apply_error_contains": "not approved"}},
{"id": "reflect-apply-already-applied", "category": "reflect", "description": "approved + 已有 apply_log → apply → idempotent 返 existing", "setup": {"seed_proposals": [{"kind": "memory_upgrade", "title": "x", "status": "approved", "body_payload": {"memory_id": "mem_seed_3", "new_claim": "y", "new_authority": "explicit"}}], "seed_memories": [{"claim": "y", "authority": "explicit", "id": "mem_seed_3"}], "seed_applies": [{"proposal_status": "success"}]}, "actions": [{"type": "reflect_propose"}, {"type": "reflect_apply"}], "expected": {"proposal_apply_result": "success"}}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test -race ./internal/memory/trustset -count=1 -v 2>&1 | tail -30`
Expected: FAIL

- [ ] **Step 3: 扩 `Action` + `Expected` + 4 个 handlers**

In `internal/memory/trustset/runner.go`：

```go
// Action struct
type Action struct {
    Type      string
    SessionID string
    Scope     string
    Extractor string
    MaxTurns  int
    ID        string  // for reflect_approve/reject/apply
    Reviewer  string
    // ... 已有
}

// Expected struct
type Expected struct {
    MemoryPresent      *bool
    ClaimMatch         string
    Authority          string
    Status             string
    EvidenceScoreGte   float64
    ExtractedMemories  []ExtractedMemory
    OldStatus          string
    NewStatus          string
    ForbidDuplicate    *bool
    ForbidOldStatusChange *bool
    ForbidActiveBeforeApprove *bool
    // 新增
    ProposalsCount   int
    ProposalKind     string
    ProposalStatus   string
    ReviewerSet      *bool
    ApplyResult      string  // "success" | "failed" | "denied_by_policy"
    ApplyErrorContains string
}

// 新 handler
func reflectApplyAction(ctx context.Context, propStore *proposal.Store, memStore *memory.Store, a Action, s Scenario) error {
    proposalID := a.ID
    if proposalID == "" { return fmt.Errorf("reflect_apply requires ID") }
    executor := proposal.NewDefaultApplyExecutor(memStore, propStore, nil, "", time.Now)
    result, err := propStore.Apply(ctx, proposalID, executor)
    if err != nil { return nil }  // expected failure → return nil, let assertExpected check
    // 期望结果在 assertExpected 里检查
    return nil
}

// runAction switch 加
case "reflect_apply":
    return reflectApplyAction(ctx, propStore, memStore, a, s)
```

- [ ] **Step 4: `assertExpected` 加 ApplyResult / ApplyErrorContains 字段**

```go
if exp.ApplyResult != "" {
    // 查 proposal_applies 表
    var result string
    err := db.QueryRowContext(ctx, `SELECT result FROM proposal_applies WHERE proposal_id = ?`, proposalID).Scan(&result)
    if err == sql.ErrNoRows || result != exp.ApplyResult {
        return false, fmt.Sprintf("apply result want %s, got %s", exp.ApplyResult, result)
    }
}
if exp.ApplyErrorContains != "" {
    var errMsg string
    db.QueryRowContext(ctx, `SELECT error FROM proposal_applies WHERE proposal_id = ?`, proposalID).Scan(&errMsg)
    if !strings.Contains(errMsg, exp.ApplyErrorContains) {
        return false, fmt.Sprintf("apply error want contains %q, got %q", exp.ApplyErrorContains, errMsg)
    }
}
```

- [ ] **Step 5: `runner_test.go` 计数 50 → 54**

- [ ] **Step 6: 跑 Trust Set 全 54 场景**

Run: `go test -race ./internal/memory/trustset -count=1 -v 2>&1 | tail -50`
Expected: 54/54 scenarios PASS

- [ ] **Step 7: 跑全 memory + app 测试**

Run: `go test -race ./internal/memory -count=1 && go test -race ./internal/app -count=1`
Expected: 全 PASS

- [ ] **Step 8: Commit**

```bash
git add internal/memory/trustset/runner.go internal/memory/trustset/runner_test.go evals/memory/trust-set-v1.json
git commit -m "feat(trustset): reflect_apply action + 4 apply scenarios (50 → 54)"
```

---

### Task 7: docs + CI

**Files:**
- Create: `docs/development/phase-4-slice-02/IMPLEMENTATION_REPORT.md`
- Modify: `README.md`（M4 Slice 02 勾选）

- [ ] **Step 1: 写实施报告**

Create `docs/development/phase-4-slice-02/IMPLEMENTATION_REPORT.md`，结构镜像 slice 01。

**6 指标 baseline 实测：**
- precision@5 ≥ slice 01 (0.30)
- false_recall = 0
- source_trace ≥ 0.98
- auth_fid = 1
- why_complete = 1
- **apply_success_rate** (NEW): 0/0 = 0.0 (v0.2 stub)

- [ ] **Step 2: 改 README**

In `README.md:114` 后加：

```markdown
- [x] M4 Slice 02：v0.2 Apply driver (memory_upgrade / agents_md / skill_draft / obsolete)（[设计稿](./docs/superpowers/specs/2026-08-26-m4-slice-02-apply-driver-design.md)、[实施报告](./docs/development/phase-4-slice-02/IMPLEMENTATION_REPORT.md)）
```

- [ ] **Step 3: 跑最终质量门禁**

```bash
gofmt -l .
go vet ./...
go test -race ./...
go test -race ./internal/memory -count=1
go test -race ./internal/memory/trustset -run TestRunner -count=1  # 54 场景
go test -race ./internal/memory/extractor -count=1
golangci-lint run ./...
govulncheck@v1.1.4 ./...
# 4 target builds
```

- [ ] **Step 4: Commit**

```bash
git add docs/development/phase-4-slice-02/IMPLEMENTATION_REPORT.md README.md
git commit -m "docs: M4 Slice 02 implementation report + README"
```

---

## Execution Order

| Order | Task | Output |
|---|---|---|
| 1 | Task 1: proposal_applies 011 migration | 审计表 + 既有测试 10 → 11 |
| 2 | Task 2: Store.Apply + ApplyResult + ApplyExecutor | 4 方法 + 4 sentinels + 3 unit test |
| 3 | Task 3: Store.UpgradeMemory + ErrMemoryAuthorityRegression | 1 方法 + 1 sentinel + 3 unit test |
| 4 | Task 4: DefaultApplyExecutor 4 路径 | 1 文件 + 1 method + 2 unit test |
| 5 | Task 5: CLI `reflect apply` | 1 函数 + 1 dispatcher case + 2 unit test + 2 sentinel 映射 |
| 6 | Task 6: Trust Set runner + 4 新场景 | 50 → 54 |
| 7 | Task 7: docs + CI | IMPLEMENTATION_REPORT + README |

## Final Gates

```bash
gofmt -l .                                      # 0
go vet ./...                                    # clean
go test -race ./...                             # 除 Windows pre-existing 外全 PASS
go test -race ./internal/memory -count=1        # memory + proposal + trustset + extractor
go test -race ./internal/agent -count=1
go test -race ./internal/app -count=1           # 含 9 TestReflect* 测试
go test -race ./internal/memory/trustset -run TestRunner -count=1  # 54 场景
golangci-lint run ./...                         # 0 issue
govulncheck@v1.1.4 ./...                        # No vulns
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64  go build ./cmd/...
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64  go build ./cmd/...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64  go build ./cmd/...
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64  go build ./cmd/...
```

## Beads Close

CI 全过、user 审核 merge 后：

```bash
bd close mengdie-3d3 --reason="M4 Slice 02 完成：7 task 全部 ship，proposal_applies 011 migration + Store.Apply + UpgradeMemory + DefaultApplyExecutor 4 路径 + CLI 5 子命令 + Trust Set 4 新场景，54 Trust Set 场景 baseline 6 指标（含 apply_success_rate stub），4 目标构建 + golangci-lint + govulncheck 全过，PR ready-for-review"
```