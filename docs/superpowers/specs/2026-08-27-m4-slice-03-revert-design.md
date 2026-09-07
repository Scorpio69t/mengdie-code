# M4 Slice 03 — Apply Revert (v0.2 audit-only) 设计

> 状态：草稿，待用户按 §分段确认后转入 writing-plans。
> 日期：2026-08-27
> 关联 Beads：（将创建 `mengdie-xxx`）
> 关联 spec：`docs/superpowers/specs/2026-08-26-m4-slice-02-apply-driver-design.md` §1.3

---

## 1. 背景与目标

### 1.1 背景

M4 Slice 02 (PR #46, merged at `ea784ce`) 落地了 v0.2 Apply driver：4 apply 路径 + `Store.Apply` + `mengdie reflect apply <id>` CLI 子命令 + 4 Trust Set 场景。**但 apply 不可逆**：

> 切片 02 §1.3 明确延期：「Apply 撤销 / Rollback（v0.3）」；§Follow-up 列入 4 项 non-blocking 之一。

已 apply 的 proposal 写进 `proposal_applies` 表，但缺一个反向操作让用户「反悔」。v0.3 v0.4 才会有真正的 content-snapshot-based rollback；本切片做 **v0.2 audit-only** 简化版。

### 1.2 目标

落地 `Store.Revert` + `mengdie reflect revert <id>`：

1. **schema 加 `reverted_at` + `reverted_by` 2 列** 到 `proposal_applies`
2. **`Store.Revert(ctx, proposalID, reviewer)`** 公开方法：检查 + 标记 audit row 为 reverted
3. **CLI `mengdie reflect revert <id>`** 子命令
4. **Trust Set 1-2 个新场景** 覆盖 revert happy path + double-revert 拒绝
5. **docs/development/phase-4-slice-03/IMPLEMENTATION_REPORT.md** + README 勾选

### 1.3 不在本切片范围（明确写出避免范围漂移）

- **真正的 content-snapshot rollback**（v0.3）：对 memory_upgrade 反向回滚到 previous claim / 对 agents_md_revision / skill_draft 恢复前文件内容
- **policy.Engine 抽 shared package**（独立 follow-up）
- **Patch Journal 集成** 替换 `os.WriteFile`（独立 follow-up）
- **Run-time policy 接入**（独立 follow-up）
- **revert-after-delete** 场景（v0.3 边缘 case）

---

## 2. 数据模型

### 2.1 新增 migration `012_proposal_applies_revert.sql`

```sql
ALTER TABLE proposal_applies ADD COLUMN reverted_at TEXT;
ALTER TABLE proposal_applies ADD COLUMN reverted_by TEXT;
```

> **v0.2 简化**：`proposal_applies` 表加 2 列（reverted_at + reverted_by），不另起新表。audit 状态由 (result + reverted_at) 二元组决定：
> - `result=success` + `reverted_at IS NULL` → 活跃
> - `result=success` + `reverted_at 非 NULL` → 已撤销
> - `result=failed` + `reverted_at 非 NULL` → failed 也可 revert（让 audit 完整）

### 2.2 `Store.Revert` 公开方法

```go
// Revert marks the apply audit row as reverted. v0.2 audit-only — does NOT
// undo the actual side effect (memory upgrade / archive / file write).
// The actual rollback is v0.3 follow-up.
//
// Refuses: proposal not applied (no proposal_applies row) OR already reverted.
// Returns: updated ApplyResult (re-fetched from proposal_applies).
func (s *Store) Revert(ctx context.Context, proposalID, reviewer string) (ApplyResult, error)
```

新增 sentinel：

```go
var (
    ErrProposalNotApplied     = errors.New("proposal has not been applied")
    ErrProposalAlreadyReverted = errors.New("proposal already reverted")
)
```

`Revert` SQL：

```sql
UPDATE proposal_applies
SET reverted_at = ?, reverted_by = ?
WHERE proposal_id = ? AND reverted_at IS NULL
```

> 0 rows affected → 返 `ErrProposalNotApplied`（row 不存在）或 `ErrProposalAlreadyReverted`（已 revert）。SQL 区分：
> - 完全无 row → `ErrProposalNotApplied`
> - 有 row 但 `reverted_at` 已 non-null → `ErrProposalAlreadyReverted`

### 2.3 修 `Store.Apply` 文档

不改 `Store.Apply` 实现。仅 doc-comment 加一行：

```go
// Apply ... (existing doc). To audit-mark a reverted apply, use Store.Revert.
```

---

## 3. CLI 设计

### 3.1 dispatcher 加 `case "revert"`

In `internal/app/reflect.go` 的 `dispatchReflect` switch，加：

```go
case "revert":
    return runReflectRevert(ctx, args[1:], a, stdout, stderr)
```

> Task 4 fix (slice 01) 已让 `--flag` 直通 `runReflect`。`revert` 是子动词（无 `-` 前缀），走 switch 路径。

### 3.2 `runReflectRevert`

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

### 3.3 `ApplyResult` 加 2 字段

```go
type ApplyResult struct {
    // ... 既有 8 字段
    RevertedAt *time.Time `json:"reverted_at,omitempty"`
    Reviewer   string     `json:"reviewer,omitempty"`  // 既有 Approve reviewer；此处复用表 reverted_by
}
```

> **设计注**：`Reviewer` 字段之前用于 `ApplyResult`（v0.2 没用上但已加）。本切片复用为 reverted_by（两者语义一致：执行反转操作的人）。`RevertedAt *time.Time` 新增。

### 3.4 exit 码映射

`exitForStoreError` 加：

```go
case errors.Is(err, memoryproposal.ErrProposalNotApplied):
    return ExitNotFound  // 3
case errors.Is(err, memoryproposal.ErrProposalAlreadyReverted):
    return ExitInvalidInput  // 2
```

---

## 4. Trust Set 增量场景

Append to `evals/memory/trust-set-v1.json` `tasks` array：

| ID | description | setup | actions | expected |
|---|---|---|---|---|
| `reflect-revert-success` | approved + applied → revert → success | `{seed_proposals: [applied], seed_memories: [y], seed_applies: [success]}` | `[reflect_propose, reflect_approve, reflect_apply, reflect_revert]` | `{proposal_revert_result: "success", proposal_reverted_set: true}` |
| `reflect-revert-fails-already-reverted` | approved + applied + already reverted → revert → 拒绝 | `{seed_applies: [success, reverted]}` (两条 audit row, 第二条 reverted_at 非 null) | `[reflect_revert]` | `{proposal_revert_result: "failed", revert_error_contains: "already reverted"}` |
| `reflect-revert-fails-not-applied` | 仅有 proposal 无 apply row → revert → 拒绝 | `{seed_proposals: [proposed]}` | `[reflect_revert]` | `{proposal_revert_result: "failed", revert_error_contains: "not applied"}` |

3 新场景（54 → 57）。

Trust Set runner 扩：
- `Action.Type` 加 `reflect_revert` verb
- `Expected.ProposalRevertResult string`（`"success"` / `"failed"`）
- `Expected.RevertErrorContains string`（substring match）
- `Expected.RevertedSet *bool`（`true` 期望 `proposal_applies.reverted_at` 非 null）
- `assertExpected` 加 3 个新字段断言

---

## 5. 不在本切片范围

- 真正的 content-snapshot rollback（v0.3）
- Batch revert（v0.3 一次撤销多个 proposal）
- Revert 之后能否再 apply（v0.2 简单：可以；v0.3 评估）
- policy.Engine 抽 shared package / Patch Journal / Run-time policy 接入（独立 follow-up）
- LLM-based Reflect / Daemon / 跨 scope consolidation

---

## 6. 风险与回滚

| 风险 | 缓解 |
|---|---|
| v0.2 audit-only → 用户误以为 revert 真撤销了 | CLI output 明确说"marked as reverted"；doc-comment 写"v0.2 does NOT undo the actual side effect" |
| 重复 revert → 拒绝 | `ErrProposalAlreadyReverted` + 2 column NOT NULL check |
| 撤销后 apply 记录历史丢失 | 保留原 `proposal_applies` row；`reverted_at` 是 in-place UPDATE 不删 |

---

## 7. 验收标准（AC）

1. **migration 012** 通过；既有 008-011 链 load 测试不变
2. **`Store.Revert` happy path**：seed applied proposal → Revert → `reverted_at` 非 null
3. **`Store.Revert` 拒绝重复**：`ErrProposalAlreadyReverted`
4. **`Store.Revert` 拒绝未应用**：`ErrProposalNotApplied`
5. **CLI `reflect revert <id>` 5 子命令**端到端（含原 4）
6. **Trust Set 57 场景**：54 旧 + 3 新全过；baseline 5 指标不退化
7. **本地质量门禁**：`gofmt -l .` 0 / `go vet ./...` clean / 4 目标 build OK / golangci-lint 0 / govulncheck no vulns
8. **docs**：`IMPLEMENTATION_REPORT.md` + `README.md` 勾选

---

## 8. 文件清单（task 拆分前的预估）

### 新增

- `internal/session/migrations/012_proposal_applies_revert.sql`
- `docs/development/phase-4-slice-03/IMPLEMENTATION_REPORT.md`

### 修改

- `internal/memory/proposal/proposal.go` — `ApplyResult` 加 `RevertedAt *time.Time` + 2 新 sentinels
- `internal/memory/proposal/store.go` — `Revert` 公开方法 + `getApplyResult` 加 2 列
- `internal/memory/proposal/proposal_test.go` — `TestStoreRevert*` 系列
- `internal/app/reflect.go` — `dispatchReflect` 加 `case "revert"` + `runReflectRevert`
- `internal/app/reflect_test.go` — 2 个 revert tests
- `internal/app/memory.go` — `exitForStoreError` 加 2 sentinel 映射
- `internal/memory/trustset/runner.go` — `reflect_revert` action verb + Expected 3 字段 + assertExpected 扩展
- `internal/memory/trustset/runner_test.go` — count 54 → 57
- `evals/memory/trust-set-v1.json` — 3 新场景
- `internal/session/sqlite_store_test.go` — migration count 11 → 12 + add `reverted_at` to column existence (可选)
- `README.md` — M4 Slice 03 勾选

### 不改

- 008-011 migration
- `internal/memory/retrieve.go` scoreRecall
- `internal/agent/`
- go.mod / go.sum

---

## 9. 分段确认

请按 § 分段 review，每段 ack 或给修改建议：

- [ ] §1 背景与目标
- [ ] §2 数据模型（migration 012 + Revert 方法）
- [ ] §3 CLI 设计（`reflect revert` + exit 码）
- [ ] §4 Trust Set 3 新场景
- [ ] §5 不在范围
- [ ] §6 风险
- [ ] §7 验收标准
- [ ] §8 文件清单

收到全部 ack 后转入 writing-plans 写实施计划。