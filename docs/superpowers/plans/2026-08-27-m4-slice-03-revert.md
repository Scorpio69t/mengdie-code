# M4 Slice 03 Implementation Plan — Apply Revert (v0.2 audit-only)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 M4 Slice 02 follow-up #2：apply 撤销 audit-only 版本。`proposal_applies` 表加 2 列 + `Store.Revert` 公开方法 + `mengdie reflect revert <id>` CLI 子命令 + Trust Set 3 新场景。54 → 57 场景。**v0.2 不撤销实际副作用**（v0.3 才上 content-snapshot rollback）。

**Architecture:** 新 migration `012_proposal_applies_revert.sql` 加 2 列；`Store.Revert` 公开方法（仅 audit marker 不真撤销）；CLI 5 子命令；Trust Set 3 新场景。

**Tech Stack:** Go 1.26.6, `modernc.org/sqlite`.

## Global Constraints

- Go 1.26.6，module `github.com/Scorpio69t/mengdie-code`
- 仅 `CGO_ENABLED=0` 跨平台
- 禁止在用户仓库中自动 git commit / push
- 任何 `git commit` 由执行人显式触发
- v0.2 audit-only：Revert 仅标 audit row 为 reverted，**不撤销 memory / file 实际修改**
- 错误统一用 `errors.New(...)` + `fmt.Errorf("%w", sentinel)`
- 不引入新第三方依赖

---

## File Structure

### 新增

- `internal/session/migrations/012_proposal_applies_revert.sql`
- `docs/development/phase-4-slice-03/IMPLEMENTATION_REPORT.md`

### 修改

- `internal/memory/proposal/proposal.go` — `ApplyResult` 加 `RevertedAt *time.Time` + 2 新 sentinels
- `internal/memory/proposal/store.go` — `Revert` 公开方法 + `getApplyResult` 返 2 新字段
- `internal/memory/proposal/proposal_test.go` — `TestStoreRevert*` 系列
- `internal/app/reflect.go` — `dispatchReflect` 加 `case "revert"` + `runReflectRevert`
- `internal/app/reflect_test.go` — 2 个 revert tests
- `internal/app/memory.go` — `exitForStoreError` 加 2 sentinel 映射
- `internal/session/sqlite_store_test.go` — migration count 11 → 12 (可选)
- `internal/memory/trustset/runner.go` — `reflect_revert` action verb + 3 Expected 字段 + assertExpected 扩展
- `internal/memory/trustset/runner_test.go` — count 54 → 57
- `evals/memory/trust-set-v1.json` — 3 新场景
- `README.md` — M4 Slice 03 勾选

### 不改

- 008-011 migration
- `internal/memory/retrieve.go` scoreRecall
- `internal/agent/`
- go.mod / go.sum

---

### Interfaces Introduced

```go
// internal/memory/proposal/proposal.go (extended)
type ApplyResult struct {
    // ... 既有 8 字段
    RevertedAt *time.Time `json:"reverted_at,omitempty"`
    Reviewer   string     `json:"reviewer,omitempty"`
}

var (
    ErrProposalNotApplied     = errors.New("proposal has not been applied")
    ErrProposalAlreadyReverted = errors.New("proposal already reverted")
)

// internal/memory/proposal/store.go (extended)
func (s *Store) Revert(ctx context.Context, proposalID, reviewer string) (ApplyResult, error)
```

---

### Task 1: migration 012 + `Store.Revert` + sentinels

**Files:**
- Create: `internal/session/migrations/012_proposal_applies_revert.sql`
- Modify: `internal/memory/proposal/proposal.go` (ApplyResult + 2 sentinels)
- Modify: `internal/memory/proposal/store.go` (Revert method + getApplyResult update)
- Modify: `internal/session/sqlite_store_test.go` (migration count 11 → 12)
- Modify: `internal/memory/proposal/proposal_test.go` (TestStoreRevert*)

- [ ] **Step 1: 写失败测试**

In `internal/memory/proposal/proposal_test.go`，加：

```go
func TestStoreRevertAppliedProposal(t *testing.T) {
    ctx := context.Background()
    store, sessionStore := openProposalStore(t)
    defer func() { _ = sessionStore.Close() }()

    p := proposal.Proposal{
        Kind: proposal.KindMemoryUpgrade, Title: "t",
        Body: proposal.ProposalBody{Kind: "memory_upgrade",
            Payload: map[string]any{"memory_id": "mem_x", "new_claim": "y", "new_authority": "explicit"}},
        Status: proposal.StatusApproved, ObservedAt: proposalTestTime,
    }
    saved, _ := store.Insert(ctx, p)
    store.UpdateStatus(ctx, saved.ID, proposal.StatusApproved, "test")

    // Manually insert apply row (since we don't have Apply executor here)
    _, err := store.DB().ExecContext(ctx,
        `INSERT INTO proposal_applies (id, proposal_id, kind, target, result, applied_at) VALUES (?, ?, ?, ?, ?, ?)`,
        "apply_"+"x", saved.ID, "memory_upgrade", "mem_x", "success", "2026-08-27T00:00:00Z",
    )
    if err != nil { t.Fatalf("insert apply: %v", err) }

    result, err := store.Revert(ctx, saved.ID, "reviewer1")
    if err != nil { t.Fatalf("Revert: %v", err) }
    if result.RevertedAt == nil { t.Fatal("RevertedAt empty") }
    if result.Reviewer != "reviewer1" { t.Fatalf("Reviewer want reviewer1, got %s", result.Reviewer) }
}

func TestStoreRevertFailsNotApplied(t *testing.T) {
    ctx := context.Background()
    store, sessionStore := openProposalStore(t)
    defer func() { _ = sessionStore.Close() }()

    p := proposal.Proposal{
        Kind: proposal.KindMemoryUpgrade, Title: "t",
        Body: proposal.ProposalBody{Kind: "memory_upgrade"},
        Status: proposal.StatusApproved, ObservedAt: proposalTestTime,
    }
    saved, _ := store.Insert(ctx, p)

    _, err := store.Revert(ctx, saved.ID, "reviewer1")
    if !errors.Is(err, proposal.ErrProposalNotApplied) {
        t.Fatalf("want ErrProposalNotApplied, got %v", err)
    }
}

func TestStoreRevertFailsAlreadyReverted(t *testing.T) {
    ctx := context.Background()
    store, sessionStore := openProposalStore(t)
    defer func() { _ = sessionStore.Close() }()

    p := proposal.Proposal{
        Kind: proposal.KindMemoryUpgrade, Title: "t",
        Body: proposal.ProposalBody{Kind: "memory_upgrade"},
        Status: proposal.StatusApproved, ObservedAt: proposalTestTime,
    }
    saved, _ := store.Insert(ctx, p)

    // Insert already-reverted apply row
    _, _ = store.DB().ExecContext(ctx,
        `INSERT INTO proposal_applies (id, proposal_id, kind, target, result, applied_at, reverted_at, reverted_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
        "apply_y", saved.ID, "memory_upgrade", "mem_x", "success",
        "2026-08-27T00:00:00Z", "2026-08-27T01:00:00Z", "old_reviewer",
    )

    _, err := store.Revert(ctx, saved.ID, "new_reviewer")
    if !errors.Is(err, proposal.ErrProposalAlreadyReverted) {
        t.Fatalf("want ErrProposalAlreadyReverted, got %v", err)
    }
}
```

Update `internal/session/sqlite_store_test.go`:
- Bump migration count 11 → 12
- (Optional) add `reverted_at` to a column-existence list

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory/proposal -run "TestStoreRevert" -count=1 -v`
Expected: FAIL

- [ ] **Step 3: 写 migration 012**

Create `internal/session/migrations/012_proposal_applies_revert.sql`:

```sql
-- 012_proposal_applies_revert.sql
-- M4 Slice 03: apply 审计表加 revert 标记列。v0.2 audit-only — 不撤销实际副作用。
ALTER TABLE proposal_applies ADD COLUMN reverted_at TEXT;
ALTER TABLE proposal_applies ADD COLUMN reverted_by TEXT;
```

- [ ] **Step 4: 实现 `ApplyResult.RevertedAt` + 2 sentinels**

In `internal/memory/proposal/proposal.go`，加：

```go
type ApplyResult struct {
    // ... 既有 8 字段
    RevertedAt *time.Time `json:"reverted_at,omitempty"`
    Reviewer   string     `json:"reviewer,omitempty"`
}

var (
    ErrProposalNotApplied     = errors.New("proposal has not been applied")
    ErrProposalAlreadyReverted = errors.New("proposal already reverted")
)
```

- [ ] **Step 5: 实现 `Store.Revert` + 改 `getApplyResult`**

In `internal/memory/proposal/store.go`，加：

```go
// Revert marks the apply audit row as reverted. v0.2 audit-only — does NOT
// undo the actual side effect (memory upgrade / archive / file write).
// The actual rollback is v0.3 follow-up.
//
// Refuses: proposal not applied (no proposal_applies row) OR already reverted.
func (s *Store) Revert(ctx context.Context, proposalID, reviewer string) (ApplyResult, error) {
    // 1. Check row exists
    var revertedAt sql.NullString
    err := s.db.QueryRowContext(ctx,
        `SELECT reverted_at FROM proposal_applies WHERE proposal_id = ?`, proposalID,
    ).Scan(&revertedAt)
    if errors.Is(err, sql.ErrNoRows) {
        return ApplyResult{}, fmt.Errorf("%w: %s", ErrProposalNotApplied, proposalID)
    }
    if err != nil { return ApplyResult{}, err }

    // 2. Check not already reverted
    if revertedAt.Valid && revertedAt.String != "" {
        return ApplyResult{}, fmt.Errorf("%w: %s", ErrProposalAlreadyReverted, proposalID)
    }

    // 3. Update
    stamp := formatStamp(s.now().UTC())
    _, err = s.db.ExecContext(ctx,
        `UPDATE proposal_applies SET reverted_at = ?, reverted_by = ? WHERE proposal_id = ? AND reverted_at IS NULL`,
        stamp, reviewer, proposalID,
    )
    if err != nil { return ApplyResult{}, fmt.Errorf("revert proposal apply: %w", err) }

    // 4. Re-fetch
    return s.getApplyResult(ctx, proposalID)
}
```

Update `getApplyResult` (Task 2 of slice 02) 读 `reverted_at` + `reverted_by` 字段：

```go
func (s *Store) getApplyResult(ctx context.Context, proposalID string) (ApplyResult, error) {
    var r ApplyResult
    var errMsg, patchID, revertedAt, revertedBy sql.NullString
    var appliedAt string
    err := s.db.QueryRowContext(ctx,
        `SELECT id, proposal_id, kind, target, result, error, applied_at, patch_id, reverted_at, reverted_by
         FROM proposal_applies WHERE proposal_id = ?`, proposalID,
    ).Scan(&r.ProposalID, &r.ProposalID, &r.Kind, &r.Target, &r.Result, &errMsg, &appliedAt, &patchID, &revertedAt, &revertedBy)
    if errors.Is(err, sql.ErrNoRows) { return ApplyResult{}, ErrProposalNotFound }
    if err != nil { return ApplyResult{}, err }
    r.Error = errMsg.String
    r.PatchID = patchID.String
    r.AppliedAt, _ = time.Parse(time.RFC3339Nano, appliedAt)
    if revertedAt.Valid && revertedAt.String != "" {
        t, _ := time.Parse(time.RFC3339Nano, revertedAt.String)
        r.RevertedAt = &t
    }
    r.Reviewer = revertedBy.String
    return r, nil
}
```

> **注**: `Reviewer` 字段复用为 `reverted_by`（两者语义一致：执行操作的人）。ApplyResult 文档需说明。

- [ ] **Step 6: 跑测试确认 pass**

Run: `go test ./internal/memory/proposal -count=1 -v`
Expected: PASS

- [ ] **Step 7: 跑全 memory 测试**

Run: `go test -race ./internal/memory -count=1`
Expected: 全 PASS

- [ ] **Step 8: Commit**

```bash
git add internal/session/migrations/012_proposal_applies_revert.sql \
        internal/memory/proposal/proposal.go \
        internal/memory/proposal/store.go \
        internal/memory/proposal/proposal_test.go \
        internal/session/sqlite_store_test.go
git commit -m "feat(memory): Store.Revert with reverted_at audit marker"
```

---

### Task 2: CLI `mengdie reflect revert <id>` 子命令

**Files:**
- Modify: `internal/app/reflect.go` — `dispatchReflect` add `case "revert"` + `runReflectRevert`
- Modify: `internal/app/reflect_test.go` — 2 tests
- Modify: `internal/app/memory.go` — `exitForStoreError` add 2 sentinel mappings

- [ ] **Step 1: 写失败测试**

In `internal/app/reflect_test.go`，加：

```go
func TestReflectRevertAppliedProposal(t *testing.T) {
    state := setupAppTestState(t)
    propStore, sessionStore := openTestProposalStore(t)
    defer func() { _ = sessionStore.Close() }()

    // seed applied proposal (raw SQL insert for apply row)
    saved, _ := propStore.Insert(context.Background(), proposal.Proposal{
        Kind: proposal.KindMemoryUpgrade, Title: "x",
        Body: proposal.ProposalBody{Kind: "memory_upgrade"},
        Status: proposal.StatusApproved, ObservedAt: proposalTestTime,
    })
    _, _ = sessionStore.DB().ExecContext(context.Background(),
        `INSERT INTO proposal_applies (id, proposal_id, kind, target, result, applied_at) VALUES (?, ?, ?, ?, ?, ?)`,
        "apply_x", saved.ID, "memory_upgrade", "mem_x", "success", "2026-08-27T00:00:00Z",
    )

    code := runApp(state, []string{"reflect", "revert", saved.ID})
    if code != ExitOK { t.Fatalf("revert exit=%d stderr=%q", code, state.stderr.String()) }
    if !strings.Contains(state.stdout.String(), "reverted " + saved.ID) {
        t.Fatalf("revert output missing: %q", state.stdout.String())
    }
}

func TestReflectRevertFailsNotApplied(t *testing.T) {
    state := setupAppTestState(t)
    propStore, sessionStore := openTestProposalStore(t)
    defer func() { _ = sessionStore.Close() }()

    saved, _ := propStore.Insert(context.Background(), proposal.Proposal{
        Kind: proposal.KindMemoryUpgrade, Title: "x",
        Body: proposal.ProposalBody{Kind: "memory_upgrade"},
        Status: proposal.StatusApproved, ObservedAt: proposalTestTime,
    })
    // no apply row inserted

    code := runApp(state, []string{"reflect", "revert", saved.ID})
    if code != ExitNotFound {
        t.Fatalf("revert want exit %d, got %d stderr=%q", ExitNotFound, code, state.stderr.String())
    }
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/app -run "TestReflectRevert" -count=1 -v`
Expected: FAIL

- [ ] **Step 3: 实现 `runReflectRevert`**

In `internal/app/reflect.go`，加：

```go
func runReflectRevert(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
    if len(args) != 1 {
        fmt.Fprintf(stderr, "用法：mengdie reflect revert <id>\n")
        return ExitInvalidInput
    }
    id := args[0]

    store, _, _, code := a.openProposalStore(ctx, common)
    if code != ExitOK { return code }
    defer ...

    reviewer := os.Getenv("USER")
    if reviewer == "" { reviewer = "mengdie" }

    result, err := store.Revert(ctx, id, reviewer)
    if err != nil { return exitForStoreError(err) }

    fmt.Fprintf(stdout, "reverted %s: kind=%s target=%s reverted_at=%s reviewer=%s\n",
        result.ProposalID, result.Kind, result.Target,
        result.RevertedAt.Format(time.RFC3339), result.Reviewer)
    return ExitOK
}
```

- [ ] **Step 4: `dispatchReflect` 加 `case "revert"`**

In `internal/app/reflect.go` `dispatchReflect` switch，加：

```go
case "revert":
    return runReflectRevert(ctx, args[1:], a, stdout, stderr)
```

- [ ] **Step 5: `exitForStoreError` 加 2 sentinel 映射**

In `internal/app/memory.go`，加：

```go
case errors.Is(err, memoryproposal.ErrProposalNotApplied):
    return ExitNotFound
case errors.Is(err, memoryproposal.ErrProposalAlreadyReverted):
    return ExitInvalidInput
```

- [ ] **Step 6: 跑测试确认 pass**

Run: `go test ./internal/app -run "TestReflectRevert" -count=1 -v`
Expected: PASS

- [ ] **Step 7: 跑全 app 测试**

Run: `go test -race ./internal/app -count=1`
Expected: 全 PASS

- [ ] **Step 8: Commit**

```bash
git add internal/app/reflect.go internal/app/reflect_test.go internal/app/memory.go
git commit -m "feat(cli): mengdie reflect revert <id> subcommand"
```

---

### Task 3: Trust Set runner + 3 新场景 + IMPLEMENTATION_REPORT

**Files:**
- Modify: `internal/memory/trustset/runner.go` (`reflect_revert` action verb + 3 Expected fields)
- Modify: `internal/memory/trustset/runner_test.go` (count 54 → 57)
- Modify: `evals/memory/trust-set-v1.json` (3 new scenarios)
- Create: `docs/development/phase-4-slice-03/IMPLEMENTATION_REPORT.md`
- Modify: `README.md` (M4 Slice 03 勾选)

- [ ] **Step 1: 写失败测试 + 3 新场景**

Append to `evals/memory/trust-set-v1.json`:

```json
{"id": "reflect-revert-success", "category": "reflect", "description": "applied → revert → success", "setup": {"seed_proposals": [{"kind": "memory_upgrade", "title": "x", "status": "approved"}], "seed_applies": [{"result": "success"}]}, "actions": [{"type": "reflect_propose"}, {"type": "reflect_revert"}], "expected": {"proposal_revert_result": "success", "proposal_reverted_set": true}},
{"id": "reflect-revert-fails-already-reverted", "category": "reflect", "description": "already reverted → revert → 拒绝", "setup": {"seed_proposals": [{"kind": "memory_upgrade", "title": "y", "status": "approved"}], "seed_applies": [{"result": "success", "reverted": true}]}, "actions": [{"type": "reflect_propose"}, {"type": "reflect_revert"}], "expected": {"proposal_revert_result": "failed", "revert_error_contains": "already reverted"}},
{"id": "reflect-revert-fails-not-applied", "category": "reflect", "description": "no apply row → revert → 拒绝", "setup": {"seed_proposals": [{"kind": "memory_upgrade", "title": "z", "status": "approved"}]}, "actions": [{"type": "reflect_propose"}, {"type": "reflect_revert"}], "expected": {"proposal_revert_result": "failed", "revert_error_contains": "not applied"}}
```

Update `internal/memory/trustset/runner_test.go` count 54 → 57.

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test -race ./internal/memory/trustset -count=1 -v 2>&1 | tail -20`
Expected: FAIL

- [ ] **Step 3: 扩 `Expected` + 1 个 action verb**

In `internal/memory/trustset/runner.go`：

```go
// Expected struct 新字段
type Expected struct {
    // ... 既有
    ProposalRevertResult string
    RevertErrorContains   string
    ProposalRevertedSet   *bool
}

// 新 handler
func reflectRevertAction(ctx context.Context, propStore *proposal.Store, ss *session.SQLiteStore, a Action, s Scenario) error {
    proposalID := a.ID
    if proposalID == "" {
        // 读 setup.seed_proposals[0] 找 id
        // ... (与 slice 02 Task 6 相同 pattern)
    }
    reviewer := a.Reviewer
    if reviewer == "" { reviewer = "trustset" }
    _, err := propStore.Revert(ctx, proposalID, reviewer)
    if err != nil { return nil }  // expected failure → let assertExpected check
    return nil
}

// runAction switch
case "reflect_revert":
    return reflectRevertAction(ctx, propStore, sessionStore, a, s)
```

Update `assertExpected`：

```go
if exp.ProposalRevertResult != "" {
    var revertedAt sql.NullString
    var result string
    err := db.QueryRowContext(ctx,
        `SELECT result, reverted_at FROM proposal_applies WHERE proposal_id = ?`, proposalID,
    ).Scan(&result, &revertedAt)
    if err == sql.ErrNoRows {
        return false, "proposal_applies row not found"
    }
    // 检查 result 字段（failure 时 result 可能是 'failed'）
    if exp.ProposalRevertResult == "success" {
        if !revertedAt.Valid || revertedAt.String == "" {
            return false, "expected reverted_at set, got NULL"
        }
    }
}
if exp.RevertErrorContains != "" {
    var errMsg string
    db.QueryRowContext(ctx, `SELECT error FROM proposal_applies WHERE proposal_id = ?`, proposalID).Scan(&errMsg)
    if !strings.Contains(errMsg, exp.RevertErrorContains) {
        return false, fmt.Sprintf("revert error want contains %q, got %q", exp.RevertErrorContains, errMsg)
    }
}
if exp.ProposalRevertedSet != nil {
    var revertedAt sql.NullString
    db.QueryRowContext(ctx, `SELECT reverted_at FROM proposal_applies WHERE proposal_id = ?`, proposalID).Scan(&revertedAt)
    isSet := revertedAt.Valid && revertedAt.String != ""
    if *exp.ProposalRevertedSet != isSet {
        return false, fmt.Sprintf("proposal_reverted_set want %v, got %v", *exp.ProposalRevertedSet, isSet)
    }
}
```

- [ ] **Step 4: 跑 Trust Set 全 57 场景**

Run: `go test -race ./internal/memory/trustset -count=1 -v 2>&1 | tail -50`
Expected: 57/57 scenarios PASS

- [ ] **Step 5: 跑全 memory + app 测试**

Run: `go test -race ./internal/memory -count=1 && go test -race ./internal/app -count=1`
Expected: 全 PASS

- [ ] **Step 6: 写 IMPLEMENTATION_REPORT**

Create `docs/development/phase-4-slice-03/IMPLEMENTATION_REPORT.md`，结构镜像 slice 02。

- [ ] **Step 7: 改 README**

In `README.md:115` 后加：

```markdown
- [x] M4 Slice 03：Apply Revert v0.2 audit-only（[设计稿](./docs/superpowers/specs/2026-08-27-m4-slice-03-revert-design.md)、[实施报告](./docs/development/phase-4-slice-03/IMPLEMENTATION_REPORT.md)）
```

- [ ] **Step 8: Commit**

```bash
git add internal/memory/trustset/runner.go internal/memory/trustset/runner_test.go evals/memory/trust-set-v1.json docs/development/phase-4-slice-03/IMPLEMENTATION_REPORT.md README.md
git commit -m "feat(trustset): reflect_revert + 3 scenarios + docs (54 → 57)"
```

---

## Execution Order

| Order | Task | Output |
|---|---|---|
| 1 | Task 1: migration 012 + Store.Revert + sentinels | 2 字段 + Revert 方法 + 3 test |
| 2 | Task 2: CLI `reflect revert` | runReflectRevert + 2 sentinel 映射 + 2 test |
| 3 | Task 3: Trust Set runner + 3 新场景 + docs | 54 → 57 + IMPLEMENTATION_REPORT |

## Final Gates

```bash
gofmt -l .                                    # 0
go vet ./...                                  # clean
go test -race ./...                           # 除 Windows pre-existing 外全 PASS
go test -race ./internal/memory -count=1      # memory + proposal + trustset + extractor
go test -race ./internal/agent -count=1
go test -race ./internal/app -count=1         # 含 9 TestReflect* + 2 TestReflectRevert
go test -race ./internal/memory/trustset -run TestRunner -count=1  # 57 场景
golangci-lint run ./...                       # 0 issue (除已知 1 carryover)
govulncheck@v1.1.4 ./...                      # No vulns
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64  go build ./cmd/...
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64  go build ./cmd/...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64  go build ./cmd/...
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64  go build ./cmd/...
```

## Beads Close

CI 全过、user 审核 merge 后：

```bash
bd close <new-beads-id> --reason="M4 Slice 03 完成：3 task 全部 ship，proposal_applies 加 2 列 + Store.Revert v0.2 audit-only + CLI reflect revert 5 子动词 + Trust Set 3 新场景 54 → 57，PR ready-for-review"
```