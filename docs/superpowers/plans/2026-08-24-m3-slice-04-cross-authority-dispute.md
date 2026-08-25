# M3 Slice 04 Implementation Plan — 跨 Authority dispute 标记 + fingerprint auto-Approve 守门

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 spec §4.2 row 3（跨 Authority conflict 双方都置 disputed + `inferred` 不覆盖 `explicit`）+ Slice 03 留下的 fingerprint auto-Approve 越权守门，让 `memory why` 输出 Authority 等级差、新增 `mengdie memory conflicts` 子命令、Trust Set 加 5 个跨 Authority 场景，整体 45 场景不退化。

**Architecture:** `internal/memory/memory.go` 新增 `AuthorityRank(a) int` 纯查询函数（与 `authorityWeight` map 并行，不动 retrieval 公式）；`Store.save` 移除 Authority 跳过条件，跨 Authority 双方都置 disputed；新增 `Store.IsCrossAuthorityConflict(ctx, Memory) (bool, error)` 公开方法；`memoryWriter` interface（slice 03 引入）加第 3 方法；`applyMemoryExtraction` 在 fingerprint 命中分支前守门（与 `trustset/runner.extractAction` 同步）；CLI 渲染 `why` 加 Authority rank gap 行；新增 `mengdie memory conflicts` 子命令。

**Tech Stack:** Go 1.26.6, `modernc.org/sqlite`（既有）, `internal/memory` + `internal/memory/trustset` + `internal/agent` + `internal/app`。

## Global Constraints

- Go 1.26.6，module `github.com/Scorpio69t/mengdie-code`
- 仅 `CGO_ENABLED=0` 跨平台构建；四目标必须通过：darwin-arm64、darwin-amd64、windows-amd64、linux-amd64
- 禁止在用户仓库中自动 git commit / push
- 任何 `git commit` 由执行人显式触发
- 不修改既有 `008_memory.sql` 与 `009_memory_source_command.sql`；本切片零 schema 变更
- 不修改 `internal/memory/retrieve.go` 的 `scoreRecall` 公式；rank 与 weight 并行
- `memoryWriter` interface 扩展是 backward-compatible（`*memory.Store` 已隐式有 `IsCrossAuthorityConflict`）
- fingerprint auto-Approve 守门仅影响 fingerprint 命中分支；非 fingerprint candidate 仍走 proposed
- 错误统一用 `errors.New(...)` + `fmt.Errorf("%w", sentinel)`
- Live provider test `//go:build liveprovider`，env 缺失 SKIP
- 中文优先 package doc + 英文 inline comments

---

## File Structure

### 新增
- `docs/development/phase-3-slice-04/IMPLEMENTATION_REPORT.md`
- `internal/memory/trustset/evidence/`（自动生成，gitignored）

### 修改
- `internal/memory/memory.go` — `AuthorityRank(a Authority) int` 函数
- `internal/memory/memory_test.go` — `TestAuthorityRank`
- `internal/memory/store.go` — dispute 循环移除 Authority 跳过；新增 `IsCrossAuthorityConflict` 公开方法
- `internal/memory/store_test.go` — `TestStoreCrossAuthorityDispute` + 跨 Authority 守门相关 unit test
- `internal/agent/runtime.go` — `memoryWriter` interface 加 `IsCrossAuthorityConflict`；`applyMemoryExtraction` 守门；新增 warn 字段 `auto_approve_skipped_cross_authority_dispute`
- `internal/agent/runtime_extractor_test.go` — `TestRunAppliesExtractionSkipsAutoApproveOnCrossAuthorityConflict` + stub store 补方法
- `internal/memory/trustset/runner.go` — `extractAction` 加跨 Authority 守门
- `internal/memory/trustset/runner_test.go` — 40 → 45 计数
- `internal/app/memory.go` — `runMemoryWhy` 渲染 Authority rank gap 行；新增 `runMemoryConflicts`；dispatcher 加 `case "conflicts"`
- `internal/app/memory_test.go` — `TestMemoryConflictsList` + `TestMemoryWhyShowsAuthorityRankGap`
- `evals/memory/trust-set-v1.json` — 5 新场景
- `README.md` — M3 Slice 04 勾选
- `.github/workflows/ci.yml` — 验证即可（不需新增 step，已有 `运行 Memory Extractor`）

### 不改
- `internal/memory/retrieve.go` — `scoreRecall` 与 `authorityWeight` map 不动
- 008_memory.sql 与 009_memory_source_command.sql — 零 schema 变更
- go.mod / go.sum

---

### Interfaces Introduced

```go
// internal/memory/memory.go (new)
func AuthorityRank(a Authority) int {
    switch a {
    case AuthorityExplicit:   return 1
    case AuthorityVerified:   return 2
    case AuthorityRepository: return 3
    case AuthorityInferred:   return 4
    default:                  return math.MaxInt
    }
}

// internal/memory/store.go (new public method)
func (s *Store) IsCrossAuthorityConflict(ctx context.Context, m Memory) (bool, error)

// internal/agent/runtime.go (interface extension)
type memoryWriter interface {
    ProposeMemory(ctx context.Context, m memory.Memory) (memory.Memory, error)
    Approve(ctx context.Context, id string) error
    IsCrossAuthorityConflict(ctx context.Context, m memory.Memory) (bool, error)  // NEW
}
```

---

### Task 1: AuthorityRank 函数 + 单元测试

**Files:**
- Modify: `internal/memory/memory.go`（加 `AuthorityRank` 函数与 `math` import）
- Modify: `internal/memory/memory_test.go`（加 `TestAuthorityRank`）

**Interfaces:**
- Consumes: 既有 `Authority` 类型
- Produces: `AuthorityRank(a Authority) int` 纯函数

- [ ] **Step 1: 写失败测试**

In `internal/memory/memory_test.go`：

```go
func TestAuthorityRank(t *testing.T) {
    cases := []struct {
        a    Authority
        want int
        name string
    }{
        {AuthorityExplicit, 1, "explicit"},
        {AuthorityVerified, 2, "verified"},
        {AuthorityRepository, 3, "repository"},
        {AuthorityInferred, 4, "inferred"},
        {Authority("unknown"), math.MaxInt, "unknown_default"},
        {Authority(""), math.MaxInt, "empty_default"},
    }
    for _, c := range cases {
        if got := AuthorityRank(c.a); got != c.want {
            t.Errorf("%s: AuthorityRank(%q) = %d, want %d", c.name, c.a, got, c.want)
        }
    }
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory -run "TestAuthorityRank" -count=1 -v`
Expected: FAIL（`AuthorityRank` undefined）

- [ ] **Step 3: 实现 `AuthorityRank`**

In `internal/memory/memory.go`，找到 `Authority` 类型定义附近，加：

```go
import "math"  // 顶部

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
```

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/memory -run "TestAuthorityRank" -count=1 -v`
Expected: PASS（6 cases）

- [ ] **Step 5: 跑全 memory 测试确认无回归**

Run: `go test -race ./internal/memory -count=1`
Expected: 全 PASS（无 AuthorityRank 调用方应无影响）

- [ ] **Step 6: Commit**

```bash
git add internal/memory/memory.go internal/memory/memory_test.go
git commit -m "feat(memory): add AuthorityRank function"
```

---

### Task 2: Store 跨 Authority 冲突检测 + IsCrossAuthorityConflict 方法

**Files:**
- Modify: `internal/memory/store.go`（移除 dispute 循环的 Authority 跳过条件；新增 `IsCrossAuthorityConflict` 公开方法）
- Modify: `internal/memory/store_test.go`（加 `TestStoreCrossAuthorityDispute`）

**Interfaces:**
- Consumes: `AuthorityRank`（Task 1）
- Produces: `Store.IsCrossAuthorityConflict(ctx, Memory) (bool, error)`

- [ ] **Step 1: 写失败测试**

In `internal/memory/store_test.go`：

```go
func TestStoreCrossAuthorityDispute(t *testing.T) {
    ctx := context.Background()
    store, _, _ := openTestStore(t)

    // Seed 1 条 explicit active
    explicit, err := store.SaveUserMemory(ctx, Memory{
        Claim: "项目测试入口是 go test ./internal/memory/...",
        Scope: Scope{Kind: "project", Value: "mengdie"},
        Source: SourceRef{Type: SourceTypeUserMessage, Ref: "user:cli"},
    })
    if err != nil { t.Fatal(err) }
    if explicit.Status != StatusActive { t.Fatalf("explicit want active, got %s", explicit.Status) }

    // Save 第 2 条 explicit（不同 claim） → 应触发 conflict
    peer2, err := store.SaveUserMemory(ctx, Memory{
        Claim: "项目测试入口是 go test ./...",
        Scope: Scope{Kind: "project", Value: "mengdie"},
        Source: SourceRef{Type: SourceTypeUserMessage, Ref: "user:cli"},
    })
    if err != nil { t.Fatal(err) }

    // 双方都应 disputed
    explicit, _ = store.Get(ctx, explicit.ID)
    peer2, _ = store.Get(ctx, peer2.ID)
    if explicit.Status != StatusDisputed { t.Fatalf("explicit want disputed, got %s", explicit.Status) }
    if peer2.Status != StatusDisputed { t.Fatalf("peer2 want disputed, got %s", peer2.Status) }

    // 跨 Authority：再加 1 条 inferred proposal 不同 claim
    inferred, err := store.ProposeMemory(ctx, Memory{
        Claim: "项目测试入口是 make test",
        Scope: Scope{Kind: "project", Value: "mengdie"},
        Authority: AuthorityInferred,
        Source: SourceRef{Type: SourceTypeAgentMessage, Ref: "run1:extractor"},
    })
    if err != nil { t.Fatal(err) }

    // 3 行都应 disputed
    inferred, _ = store.Get(ctx, inferred.ID)
    if inferred.Status != StatusDisputed { t.Fatalf("inferred want disputed, got %s", inferred.Status) }

    // IsCrossAuthorityConflict 应返 true（inferred vs explicit）
    conflict, err := store.IsCrossAuthorityConflict(ctx, inferred)
    if err != nil { t.Fatal(err) }
    if !conflict { t.Fatal("IsCrossAuthorityConflict(inferred) want true") }

    // 反向：explicit 与新 candidate 也应 true
    conflict2, err := store.IsCrossAuthorityConflict(ctx, explicit)
    if err != nil { t.Fatal(err) }
    if !conflict2 { t.Fatal("IsCrossAuthorityConflict(explicit) want true") }

    // 无冲突场景：fresh store + 新 candidate → false
    freshStore, _, _ := openTestStore(t)
    mem := Memory{
        Claim: "孤立条目",
        Scope: Scope{Kind: "project", Value: "mengdie"},
        Authority: AuthorityInferred,
    }
    conflict3, err := freshStore.IsCrossAuthorityConflict(ctx, mem)
    if err != nil { t.Fatal(err) }
    if conflict3 { t.Fatal("IsCrossAuthorityConflict(isolated) want false") }
}
```

> Note: `openTestStore(t)` 可能签名不同；读现有 `store_test.go` 找正确的 setup helper（很可能是 `newTestStore` 或类似）。

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory -run "TestStoreCrossAuthorityDispute" -count=1 -v`
Expected: FAIL（`IsCrossAuthorityConflict` undefined）

- [ ] **Step 3: 实现 `IsCrossAuthorityConflict`**

In `internal/memory/store.go`，找合适位置（`save` 函数附近）加：

```go
// IsCrossAuthorityConflict reports whether m would be in cross-authority
// dispute with any active peer in the same scope whose claim differs after
// canonicalisation AND whose authority outranks m.Authority (per
// AuthorityRank). Spec §4.2 row 3 — cross-authority disputes must mark both
// sides; the higher-authority peer retains recall priority.
func (s *Store) IsCrossAuthorityConflict(ctx context.Context, m Memory) (bool, error) {
    if err := m.Scope.Valid(); err != nil {
        return false, fmt.Errorf("cross-authority conflict: %w", err)
    }
    normalized := CanonicalizeClaim(m.Claim)
    ownRank := AuthorityRank(m.Authority)
    rows, err := s.db.QueryContext(ctx,
        `SELECT id, claim, authority FROM memories
         WHERE scope_kind = ? AND scope_value = ?
           AND status = 'active'
           AND id != ?`,
        m.Scope.Kind, m.Scope.Value, m.ID,
    )
    if err != nil {
        return false, fmt.Errorf("query cross-authority peers: %w", err)
    }
    defer func() { _ = rows.Close() }()
    for rows.Next() {
        var (
            id        string
            claim     string
            authority string
        )
        if err := rows.Scan(&id, &claim, &authority); err != nil {
            return false, fmt.Errorf("scan cross-authority peer: %w", err)
        }
        if CanonicalizeClaim(claim) == normalized {
            continue
        }
        if AuthorityRank(Authority(authority)) < ownRank {
            return true, nil
        }
    }
    if err := rows.Err(); err != nil {
        return false, fmt.Errorf("iterate cross-authority peers: %w", err)
    }
    return false, nil
}
```

- [ ] **Step 4: 移除 `save` 的 Authority 跳过条件**

In `internal/memory/store.go:288-302` dispute 循环，删除 `if row.Authority != m.Authority { continue }`（slice 03 留下的跨 authority 跳过）。保留：

```go
for _, row := range existing {
    if row.ID == m.ID {
        continue
    }
    if CanonicalizeClaim(row.Claim) == normalized {
        continue
    }
    disputeIDs = append(disputeIDs, row.ID)
}
```

把注释从「same-scope + same-authority」改成「same-scope + different-claim」：

```go
// Conflict marking: any other same-scope row whose claim canonicalises
// differently gets flipped to disputed (spec §4.2 row 2 same-authority +
// row 3 cross-authority).
```

- [ ] **Step 5: 跑测试确认 pass**

Run: `go test ./internal/memory -run "TestStoreCrossAuthorityDispute" -count=1 -v`
Expected: PASS

- [ ] **Step 6: 跑全 memory 测试确认无回归**

Run: `go test -race ./internal/memory -count=1`
Expected: 全 PASS。**注意**：原 Trust Set 35 场景可能因跨 Authority 触发 dispute 而行为变化，需要在 Task 5 跑 Trust Set 时再确认 baseline 指标不退化。

- [ ] **Step 7: Commit**

```bash
git add internal/memory/store.go internal/memory/store_test.go
git commit -m "feat(memory): detect cross-authority conflicts + IsCrossAuthorityConflict"
```

---

### Task 3: memory why 输出 Authority 等级差行

**Files:**
- Modify: `internal/app/memory.go`（`runMemoryWhy` 渲染 Authority rank gap）
- Modify: `internal/app/memory_test.go`（加 `TestMemoryWhyShowsAuthorityRankGap`）

**Interfaces:**
- Consumes: `WhyReport.Conflicts []Memory`（既有）+ `AuthorityRank`（Task 1）
- Produces: 渲染新增「Authority rank + 等级差」行（仅 Conflicts 非空时）

- [ ] **Step 1: 写失败测试**

In `internal/app/memory_test.go`：

```go
func TestMemoryWhyShowsAuthorityRankGap(t *testing.T) {
    state := setupAppTestState(t)
    // seed 2 行：1 explicit + 1 inferred，跨 Authority 冲突
    code := runApp(state, []string{"memory", "remember", "项目测试入口是 go test ./internal/memory/...", "--scope", "project"})
    if code != ExitOK { t.Fatalf("remember 1 exit=%d stderr=%q", code, state.stderr.String()) }
    code = runApp(state, []string{"memory", "remember", "项目测试入口是 make test", "--scope", "project", "--authority", "inferred"})
    if code != ExitOK { t.Fatalf("remember 2 exit=%d stderr=%q", code, state.stderr.String()) }

    // 找一条 id
    state.stdout.Reset()
    code = runApp(state, []string{"memory", "list", "--status", "disputed", "--json"})
    if code != ExitOK { t.Fatalf("list exit=%d stderr=%q", code, state.stderr.String()) }
    var firstID string
    for _, line := range strings.Split(strings.TrimSpace(state.stdout.String()), "\n") {
        if line == "" { continue }
        var row struct{ ID string `json:"id"` }
        if err := json.Unmarshal([]byte(line), &row); err == nil && row.ID != "" {
            firstID = row.ID
            break
        }
    }
    if firstID == "" { t.Fatal("no disputed memory found") }

    // why should mention authority rank
    state.stdout.Reset()
    code = runApp(state, []string{"memory", "why", firstID})
    if code != ExitOK { t.Fatalf("why exit=%d stderr=%q", code, state.stderr.String()) }
    out := state.stdout.String()
    if !strings.Contains(out, "authority_rank_gap") {
        t.Fatalf("why output missing authority_rank_gap: %q", out)
    }
    if !strings.Contains(out, "rank 1") || !strings.Contains(out, "rank 4") {
        t.Fatalf("why output missing rank numbers: %q", out)
    }
}
```

> Note: `json` import 需要加；现有 `memory_test.go` 是否已 import 视情况。

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/app -run "TestMemoryWhyShowsAuthorityRankGap" -count=1 -v`
Expected: FAIL（`authority_rank_gap` 不在输出里）

- [ ] **Step 3: 改 `runMemoryWhy` 渲染**

In `internal/app/memory.go`（`runMemoryWhy` 函数附近，找到渲染 Conflicts 的循环），加：

```go
// 在 Conflicts 渲染循环末尾，若 len(report.Conflicts) > 0，打印 Authority rank 行
if len(report.Conflicts) > 0 {
    ownRank := memory.AuthorityRank(mem.Authority)
    if _, err := fmt.Fprintf(stdout, "authority_rank=%d\n", ownRank); err != nil {
        return ExitRunError
    }
    minPeerRank := ownRank
    for _, peer := range report.Conflicts {
        if r := memory.AuthorityRank(peer.Authority); r < minPeerRank {
            minPeerRank = r
        }
    }
    gap := ownRank - minPeerRank
    if gap < 0 { gap = -gap }
    winner := "own"
    if minPeerRank < ownRank { winner = "peer" }
    if _, err := fmt.Fprintf(stdout, "authority_rank_gap=%d (%s wins)\n", gap, winner); err != nil {
        return ExitRunError
    }
}
```

`ownRank` 取当前记忆的 Authority rank；`minPeerRank` 取 peer 中最小（最权威）的 rank；gap 是两者差值的绝对值；winner 是 rank 最小的一方（更权威）。

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/app -run "TestMemoryWhyShowsAuthorityRankGap" -count=1 -v`
Expected: PASS

- [ ] **Step 5: 跑全 app 测试确认无回归**

Run: `go test -race ./internal/app -count=1`
Expected: 全 PASS（既有 `TestMemoryRememberAndListRoundTrip` / `TestMemoryListStatusAutoApproved` 等不受影响）

- [ ] **Step 6: Commit**

```bash
git add internal/app/memory.go internal/app/memory_test.go
git commit -m "feat(cli): show authority rank gap in memory why output"
```

---

### Task 4: memoryWriter interface 扩展 + fingerprint auto-Approve 守门

**Files:**
- Modify: `internal/agent/runtime.go`（`memoryWriter` interface 加方法；`applyMemoryExtraction` 守门）
- Modify: `internal/agent/runtime_extractor_test.go`（新测试 + stub store 补方法）

**Interfaces:**
- Consumes: `Store.IsCrossAuthorityConflict`（Task 2）+ `AuthorityRank`（Task 1）
- Produces: fingerprint 命中但跨 Authority 冲突时不 Approve；保持 status=proposed

- [ ] **Step 1: 写失败测试**

In `internal/agent/runtime_extractor_test.go`：

```go
func TestRunAppliesExtractionSkipsAutoApproveOnCrossAuthorityConflict(t *testing.T) {
    // 复用 Task 4 的 stubStore，加 IsCrossAuthorityConflict 方法（默认返 false）
    // TestRunAppliesExtractionTwoPhaseWithAutoApprove 改：stub Store.IsCrossAuthorityConflict 返 true for fingerprint candidate
    // 期望：
    //   - ProposeMemory 调 2 次（两个 candidate）
    //   - Approve 调 0 次（被守门拦下）
    //   - result.AutoApprovedCount == 0
    //   - fingerprint candidate 仍在 proposed slice（stubStore.proposed 里）
}
```

> 具体写法参考 `task-4-report.md` 的 stubStore 结构 + `TestRunAppliesExtractionTwoPhaseWithAutoApprove` 模板。

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/agent -run "TestRunAppliesExtractionSkipsAutoApproveOnCrossAuthorityConflict" -count=1 -v`
Expected: FAIL（`Approve` 被调了 1 次而非 0 次）

- [ ] **Step 3: 扩 `memoryWriter` interface**

In `internal/agent/runtime.go`（slice 03 Task 4 加的 `memoryWriter` 定义处），加方法：

```go
type memoryWriter interface {
    ProposeMemory(ctx context.Context, m memory.Memory) (memory.Memory, error)
    Approve(ctx context.Context, id string) error
    IsCrossAuthorityConflict(ctx context.Context, m memory.Memory) (bool, error)  // NEW
}
```

`*memory.Store` 隐式满足（Task 2 已实现）；production 装配 (`internal/app/runtime.go:236`) 无需改动。

- [ ] **Step 4: `applyMemoryExtraction` 守门**

In `internal/agent/runtime.go:applyMemoryExtraction`，fingerprint 命中分支前加守门：

```go
if extractor.ShouldAutoApprove(stored.Claim) {
    conflict, err := a.memoryStore.IsCrossAuthorityConflict(extCtx, stored)
    if err != nil {
        a.warnExtraction(ctx, "auto_approve_conflict_check_failed", err)
        continue
    }
    if conflict {
        a.warnExtraction(ctx, "auto_approve_skipped_cross_authority_dispute", nil)
        continue
    }
    if err := a.memoryStore.Approve(extCtx, stored.ID); err != nil {
        a.warnExtraction(ctx, "auto_approve_approve_failed", err)
        continue
    }
    autoApprovedCount++
}
```

新增 `a.warnExtraction` 用例：`auto_approve_skipped_cross_authority_dispute`。读 `runtime.go` 现有 `warnExtraction` 实现确认（slice 03 已用 placeholder）。

- [ ] **Step 5: stub store 补方法**

In `internal/agent/runtime_extractor_test.go`，现有 `stubStore`（Task 4 引入）补：

```go
func (s *stubStore) IsCrossAuthorityConflict(ctx context.Context, m memory.Memory) (bool, error) {
    // 默认返 false；新测试通过字段覆写
    if s.conflictFn != nil {
        return s.conflictFn(ctx, m)
    }
    return false, nil
}
```

加 `conflictFn func(context.Context, memory.Memory) (bool, error)` 字段；helper `newTestAgentWithExtractorAndStore` 加对应配置参数。

- [ ] **Step 6: 跑测试确认 pass**

Run: `go test ./internal/agent -run "TestRunAppliesExtraction" -count=1 -v`
Expected: Task 4 测试 + 新测试都 PASS

- [ ] **Step 7: 跑全 agent 测试确认无回归**

Run: `go test -race ./internal/agent -count=1`
Expected: 全 PASS

- [ ] **Step 8: Commit**

```bash
git add internal/agent/runtime.go internal/agent/runtime_extractor_test.go
git commit -m "feat(agent): guard fingerprint auto-Approve against cross-authority conflict"
```

---

### Task 5: trustset runner.extractAction 同步守门 + 5 新场景

**Files:**
- Modify: `internal/memory/trustset/runner.go`（`extractAction` 加跨 Authority 守门）
- Modify: `internal/memory/trustset/runner_test.go`（计数 40 → 45）
- Modify: `evals/memory/trust-set-v1.json`（5 新场景）

**Interfaces:**
- Consumes: `Store.IsCrossAuthorityConflict`（Task 2）+ `memoryWriter` interface 扩展（Task 4）
- Produces: 5 新 Trust Set 场景覆盖跨 Authority + 守门；Trust Set 45 场景全过

- [ ] **Step 1: 写 5 新场景的 setup/expected 设计**

5 场景 JSON（参照 slice 03 5 个 auto-approved 场景格式）：

1. `cross-authority-explicit-vs-inferred` — category=explicit
   - actions: `propose_memory` 一条 explicit "go test ./internal/memory/..." (status=active) + `propose_memory` 一条 inferred "make test" (status=proposed → disputed)
   - expected: `extracted_memories: [{claim_contains: "make test", authority: inferred, status: disputed}]`

2. `cross-authority-verified-vs-inferred` — category=verified
   - setup: 模拟一次成功 `go test ./...` 落 verified "go test ./..."
   - actions: propose 一条 inferred "make test"
   - expected: 双方 disputed

3. `cross-authority-repository-vs-inferred` — category=repository
   - setup: edit_file 工具成功 → repository fact
   - actions: propose 一条 inferred "edit_file vs write_file"
   - expected: 双方 disputed

4. `auto-approve-skipped-cross-authority` — category=inferred
   - setup: explicit active "项目测试入口是 go test ./..."
   - actions: extract hybrid（fingerprint 命中 "go test" 但 conflict）
   - expected: candidate status=proposed（**不**是 auto-approved）

5. `auto-approve-still-runs-no-conflict` — category=inferred（回归测试）
   - setup: 无冲突（fresh scope）
   - actions: extract hybrid fingerprint 命中 "go test"
   - expected: candidate status=auto-approved（即 active via auto-Approve）

- [ ] **Step 2: 加场景到 JSON**

Append to `evals/memory/trust-set-v1.json` `tasks` array。结构参照末尾 5 个 slice 03 场景。

验证 JSON 合法：本机无 python3，用 `go run ./cmd/mengdie-eval` 不存在的检查；改用 `node -e "JSON.parse(require('fs').readFileSync('evals/memory/trust-set-v1.json','utf8'))"` 或写一个 Go 临时程序。

- [ ] **Step 3: `extractAction` 加守门**

In `internal/memory/trustset/runner.go`（slice 03 引入的 extractAction，约 line 515-519），fingerprint 命中分支前加：

```go
if stored.Status == memory.StatusProposed && extractor.ShouldAutoApprove(stored.Claim) {
    conflict, err := memStore.IsCrossAuthorityConflict(ctx, stored)
    if err != nil {
        return memory.Memory{}, fmt.Errorf("check cross-authority conflict: %w", err)
    }
    if !conflict {
        if approved, err := memStore.Approve(ctx, stored.ID); err != nil {
            return memory.Memory{}, fmt.Errorf("auto-approve %s: %w", stored.ID, err)
        } else {
            stored = approved
        }
    }
    // 守门命中时 stored.Status 保持 proposed
}
```

读现有 `extractAction` 上下文确认接口（`memStore` 是否就是 `*memory.Store`，需要补方法 stub）。

- [ ] **Step 4: `runner_test.go` 计数 40 → 45**

In `internal/memory/trustset/runner_test.go`，找到「35 → 40」slice 03 留下的计数断言（line 28-42 附近），改为 45；docstring 同步更新。

- [ ] **Step 5: 跑 Trust Set**

Run: `go test -race ./internal/memory/trustset -run TestRunner -count=1 -v`
Expected: 45/45 scenarios PASS；5 旧 fingerprint 场景不退化

- [ ] **Step 6: 跑全 trustset 测试确认无回归**

Run: `go test -race ./internal/memory/trustset -count=1`
Expected: 全 PASS

- [ ] **Step 7: Commit**

```bash
git add internal/memory/trustset/runner.go internal/memory/trustset/runner_test.go evals/memory/trust-set-v1.json
git commit -m "feat(trustset): guard extract action + 5 cross-authority scenarios"
```

---

### Task 6: CLI mengdie memory conflicts 子命令

**Files:**
- Modify: `internal/app/memory.go`（新增 `runMemoryConflicts` + dispatcher case）
- Modify: `internal/app/memory_test.go`（加 `TestMemoryConflictsList`）

**Interfaces:**
- Consumes: `memory.Store.List`（既有）+ `--scope` flag（既有）
- Produces: `mengdie memory conflicts` 子命令

- [ ] **Step 1: 写失败测试**

In `internal/app/memory_test.go`：

```go
func TestMemoryConflictsList(t *testing.T) {
    state := setupAppTestState(t)
    // seed 2 行 cross-authority conflict
    code := runApp(state, []string{"memory", "remember", "项目测试入口是 go test ./internal/memory/...", "--scope", "project"})
    if code != ExitOK { t.Fatalf("remember 1 exit=%d stderr=%q", code, state.stderr.String()) }
    code = runApp(state, []string{"memory", "remember", "项目测试入口是 make test", "--scope", "project", "--authority", "inferred"})
    if code != ExitOK { t.Fatalf("remember 2 exit=%d stderr=%q", code, state.stderr.String()) }

    // 跑 conflicts
    code = runApp(state, []string{"memory", "conflicts"})
    if code != ExitOK { t.Fatalf("conflicts exit=%d stderr=%q", code, state.stderr.String()) }
    out := state.stdout.String()
    if !strings.Contains(out, "peers=") {
        t.Fatalf("conflicts output missing peers column: %q", out)
    }
    if !strings.Contains(out, "disputed") {
        t.Fatalf("conflicts output missing status: %q", out)
    }
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/app -run "TestMemoryConflictsList" -count=1 -v`
Expected: FAIL（`memory conflicts` 不在 dispatcher）

- [ ] **Step 3: 加 `runMemoryConflicts`**

In `internal/app/memory.go`，模仿 `runMemoryList` 加：

```go
func runMemoryConflicts(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
    flags, common := a.newMemoryFlagSet("mengdie memory conflicts", stderr)
    scopeKind := flags.String("scope", "", "按 scope_kind 过滤")
    limit := flags.Int("limit", 0, "最大返回条数（默认 20，上限 200）")
    jsonOutput := flags.Bool("json", false, "输出 JSON Lines")
    if err := flags.Parse(args); err != nil {
        return flagExitCode(err)
    }
    if flags.NArg() != 0 {
        if err := writeMemoryError(stderr, "memory conflicts 不接受位置参数\n"); err != nil {
            return ExitRunError
        }
        return ExitInvalidInput
    }
    if *scopeKind != "" {
        if _, ok := memoryAllowedScopeKinds[*scopeKind]; !ok {
            if err := writeMemoryError(stderr, "未知 scope 类型 %q\n", *scopeKind); err != nil {
                return ExitRunError
            }
            return ExitInvalidInput
        }
    }

    memStore, sessionStore, _, code := a.openMemoryStore(ctx, common)
    if code != ExitOK { return code }
    defer func() { _ = sessionStore.Close() }()

    rows, err := memStore.List(ctx, memory.ListQuery{
        ScopeKind: *scopeKind, Status: string(memory.StatusDisputed), Limit: *limit,
    })
    if err != nil {
        if errors.Is(err, memory.ErrInvalidQuery) {
            if werr := writeMemoryError(stderr, "查询参数无效：%v\n", err); werr != nil {
                return ExitRunError
            }
            return ExitInvalidInput
        }
        return exitForStoreError(err)
    }

    // 渲染
    if *jsonOutput {
        encoder := json.NewEncoder(stdout)
        encoder.SetEscapeHTML(false)
        for _, row := range rows {
            if err := encoder.Encode(row); err != nil {
                return ExitRunError
            }
        }
        return ExitOK
    }
    return writeMemoryConflictsTable(stdout, rows, memStore, ctx)
}
```

加 helper `writeMemoryConflictsTable` 渲染 `id | claim | authority | status | peers | updated_at`。`peers` 通过 `WhyReport.Conflicts` 长度计算：每条 row 调一次 `memStore.why(ctx, row.ID)` 取 `.Conflicts` 长度。

注意性能：conflicts 子命令通常少（slice 04 期望 disputed 数 < 10），N+1 查询可接受；后续 v0.2 可优化。

- [ ] **Step 4: dispatcher 加 case**

In `internal/app/memory.go:115-139` 的 `runMemory` dispatcher switch，加：

```go
case "conflicts":
    return runMemoryConflicts(ctx, rest, a, stdout, stderr)
```

更新帮助文本 line 107：`用法：mengdie memory <list|show|why|remember|forget|supersede|approve|rebuild|export|conflicts> [选项]`

- [ ] **Step 5: 跑测试确认 pass**

Run: `go test ./internal/app -run "TestMemoryConflictsList" -count=1 -v`
Expected: PASS

- [ ] **Step 6: 跑全 app 测试确认无回归**

Run: `go test -race ./internal/app -count=1`
Expected: 全 PASS

- [ ] **Step 7: Commit**

```bash
git add internal/app/memory.go internal/app/memory_test.go
git commit -m "feat(cli): add memory conflicts subcommand"
```

---

### Task 7: Live provider + CI + 文档

**Files:**
- Create: `docs/development/phase-3-slice-04/IMPLEMENTATION_REPORT.md`
- Modify: `README.md`（勾选 M3 Slice 04）
- Modify: `.github/workflows/ci.yml`（验证即可，不需新增 step）

**Interfaces:**
- Consumes: 既有 40 Trust Set 场景 + 5 新
- Produces: 实施报告 + README 勾选

- [ ] **Step 1: 写实施报告**

Create `docs/development/phase-3-slice-04/IMPLEMENTATION_REPORT.md`，结构镜像 slice 03：

- 交付范围
- 新增 / 修改文件清单
- 关键设计与守则
- Trust Set 退出门禁（45 场景 baseline 6 指标）
- 验证
- Follow-up
- 红线检查

**6 指标 baseline 实测：**
- `precision@5` — 预期 ≥ slice 03 的 0.38
- `false_recall_rate = 0`
- `source_traceability = 1`（Trust Set 不涉及 evidence_score 计算）
- `authority_fidelity = 1`
- `why_completeness = 1`（新增 rank gap 行）
- `auto_approved_rate` — 预期 ≥ 0.60（4 个原 fingerprint 场景仍跑 auto-approve；1 个新 `auto-approve-skipped-cross-authority` 故意保持 proposed）

- [ ] **Step 2: 改 README**

In `README.md:113`，改：

```markdown
- [x] M3 Slice 04：跨 Authority dispute 标记 + fingerprint auto-Approve 守门（[设计稿](./docs/superpowers/specs/2026-08-24-m3-slice-04-cross-authority-dispute-design.md)）
```

- [ ] **Step 3: 验证 ci.yml**

`.github/workflows/ci.yml` 已有「运行 Memory Extractor」step（line 62-63，slice 02 加的）覆盖 `internal/memory/extractor/...`。本切片改动在 `internal/memory`（非 extractor）和 `internal/agent` / `internal/app` / `internal/memory/trustset`，都在 `go test -race ./...` 与「测试 · X」覆盖范围内。**无需新增 step**。

- [ ] **Step 4: 跑最终质量门禁**

```bash
gofmt -l .                       # 0
go vet ./...                    # clean
go test -race ./...             # 除 Windows pre-existing 外全 PASS
go test -race ./internal/memory/trustset -run TestRunner -count=1  # 45 场景全过
go test -race ./internal/memory/extractor/...  # 31+ 测试
golangci-lint run ./...         # 0 issue
govulncheck@v1.1.4 ./...        # No vulns
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64  go build ./cmd/...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64  go build ./cmd/...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64   go build ./cmd/...
```

- [ ] **Step 5: Commit**

```bash
git add docs/development/phase-3-slice-04/IMPLEMENTATION_REPORT.md README.md
git commit -m "docs: M3 Slice 04 implementation report + README"
```

---

## Execution Order

| Order | Task | Output |
|---|---|---|
| 1 | Task 1: AuthorityRank 函数 + 单元测试 | `internal/memory/memory.go` + 6-case unit test |
| 2 | Task 2: Store 跨 Authority 冲突检测 + IsCrossAuthorityConflict | `internal/memory/store.go` dispute 循环改 + 新公开方法 |
| 3 | Task 3: memory why 输出 Authority 等级差 | `internal/app/memory.go` 渲染 rank gap |
| 4 | Task 4: memoryWriter interface 扩展 + auto-Approve 守门 | `internal/agent/runtime.go` + stub store |
| 5 | Task 5: trustset runner.extractAction 同步守门 + 5 新场景 | `internal/memory/trustset/runner.go` + 5 JSON 场景 + 45 计数 |
| 6 | Task 6: CLI mengdie memory conflicts 子命令 | `internal/app/memory.go` 新增子命令 |
| 7 | Task 7: Live provider + CI + 文档 | IMPLEMENTATION_REPORT + README |

## Final Gates

```bash
gofmt -l .                                  # 0
go vet ./...                                # clean
go test -race ./...                         # 除 Windows pre-existing TestShellExecute 外全 PASS
go test -race ./internal/memory/...         # memory + trustset + extractor 全过；trustset 45 场景端到端
go test -race ./internal/agent/...          # 含 TestRunAppliesExtractionSkipsAutoApproveOnCrossAuthorityConflict
go test -race ./internal/app/...            # 含 TestMemoryConflictsList + TestMemoryWhyShowsAuthorityRankGap
golangci-lint run ./...                     # 0 issue
govulncheck@v1.1.4 ./...                    # No vulns
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64  go build ./cmd/...   # OK
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64  go build ./cmd/...   # OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64  go build ./cmd/...   # OK
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64  go build ./cmd/...   # OK
```

## Beads Close

CI 全过、user 审核 merge 后：

```bash
bd close mengdie-5tw --reason="M3 Slice 04 完成：7 task 全部 ship，45 Trust Set 场景 baseline 6 指标全在 [0,1]，跨 Authority dispute 双方都置 disputed，fingerprint auto-Approve 越权守门就绪，4 目标构建 + golangci-lint + govulncheck 全过，PR ready-for-review"
```