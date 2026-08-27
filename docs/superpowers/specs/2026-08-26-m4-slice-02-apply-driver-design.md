# M4 Slice 02 — v0.2 Apply Driver 设计

> 状态：草稿，待用户按 §分段确认后转入 writing-plans。
> 日期：2026-08-26
> 关联 Beads：（将创建 `mengdie-xxx`）
> 关联 spec：`docs/superpowers/specs/2026-08-26-m4-slice-01-reflect-proposal-design.md`
> 关联架构：`ARCHITECTURE.md §9.4（安全边界）`、`§9.5（价值指标）`

---

## 1. 背景与目标

### 1.1 背景

M4 Slice 01 (PR #45, merged at `33c8a77`) 落地了 Reflect v0.1：5 阶段流水线 + `proposal_acceptance_rate` stub + `mengdie reflect proposals/approve/reject` 子命令。**但 apply 链路仍是空操作**：

> `reflect approve <id>` 当前仅把 `proposal.status` 标为 `approved` + 写 `reviewer` 字段。**Approved 之后没有人实际把 proposal 应用到目标（memory / AGENTS.md / Skill）。**

arch §9.4 明确规定：
> Reflect Worker 的工具集只有：读取事件、会话、记忆和已存在的项目文件；写入独立 proposal/staging 表；生成报告。**它不能获得 edit、shell、network 或正式规则写权限。** 即使用户批准提案，实际变更仍通过普通 Policy + Approval + Patch Journal 链路执行。

slice 01 IMPLEMENTATION_REPORT §Follow-up 第 1 项："v0.2 Apply driver — `reflect approve` 后由 driver 走 Policy + Approval 链路实改 memory 或写 AGENTS.md / Skill（arch §9.5 acceptance rate gate）"。

### 1.2 目标

v0.2 Apply driver 落地的最小闭环：

1. **`proposal_applies` 审计表** + `proposal_Store.Apply(ctx, id)` 方法
2. **`mengdie reflect apply <id>`** CLI 子命令：触发 apply，输出执行结果
3. **Policy + Patch Journal 集成**：apply 走普通 Policy 链路（arch §9.4）
4. **4 个 ProposalKind 的 apply 路径**：
   - `memory_upgrade` → 调 `memory.Store` 的 `UpgradeMemory(id, newClaim, newAuthority)` API（v0.2 需新增）
   - `agents_md_revision` → 调 `policy` 通过后写 `AGENTS.md`（v0.1 简化为 propose-only；v0.2 实际写）
   - `skill_draft` → 调 `policy` 通过后写 `skills/<name>.md`（v0.1 propose-only；v0.2 实际写）
   - `obsolete` → 调 `memory.Store.Archive(id)` 标记归档
5. **Trust Set 增量场景**：1-2 个 apply happy path 场景

### 1.3 不在本切片范围（明确写出避免范围漂移）

- 自动 resolve / 自动 merge（M4 后续切片）
- Daemon / idle / cron（arch §9.2 延期）
- LLM-based Reflect（独立 follow-up）
- Policy 系统重构（v0.2 复用既有 Policy；仅在 ProposalKind=agents_md_revision / skill_draft 时走 Policy 审批）
- 撤销已 apply 的 proposal（v0.2 不可逆；v0.3 加 rollback）

---

## 2. 数据模型

### 2.1 新增 migration `011_proposal_applies.sql`

```sql
CREATE TABLE proposal_applies (
    rowid        INTEGER PRIMARY KEY,
    id           TEXT NOT NULL UNIQUE,
    proposal_id  TEXT NOT NULL UNIQUE,  -- one apply per proposal
    kind         TEXT NOT NULL,        -- mirrors reflection_proposals.kind
    target       TEXT NOT NULL,        -- memory_id | file path
    result       TEXT NOT NULL,        -- success | failed | denied_by_policy
    error        TEXT,                 -- error message on failure
    applied_at   TEXT NOT NULL,
    patch_id     TEXT,                 -- PatchJournal id (when filesystem write)
    FOREIGN KEY (proposal_id) REFERENCES reflection_proposals(id)
);

CREATE INDEX idx_proposal_applies_proposal ON proposal_applies (proposal_id);
CREATE INDEX idx_proposal_applies_applied_at ON proposal_applies (applied_at DESC);
```

### 2.2 `Proposal.ApplyStatus` 扩展

`reflection_proposals` 表加 1 个字段（v0.2 migration `012_proposal_apply_status.sql`）：

```sql
ALTER TABLE reflection_proposals ADD COLUMN applied_at TEXT;
ALTER TABLE reflection_proposals ADD COLUMN apply_result TEXT;  -- success | failed | denied
```

或者更轻：保持 `reflection_proposals` 不变，apply 状态只查 `proposal_applies` 表（1:1 join）。

> **决定**: 用轻方案 — `reflection_proposals` 不变；apply 状态独立存 `proposal_applies`。简单可读。

### 2.3 新 Go 类型（`internal/memory/proposal/`）

```go
// ApplyResult captures one apply execution
type ApplyResult struct {
    ProposalID  string
    Kind        ProposalKind
    Target      string  // memory_id or file path
    Result      string  // "success" | "failed" | "denied_by_policy"
    Error       string
    AppliedAt   time.Time
    PatchID     string
}

// ApplyExecutor runs the actual side effect (memory upgrade / file write)
type ApplyExecutor interface {
    ApplyMemoryUpgrade(ctx, proposal Proposal) (ApplyResult, error)
    ApplyAgentsMdRevision(ctx, proposal Proposal) (ApplyResult, error)
    ApplySkillDraft(ctx, proposal Proposal) (ApplyResult, error)
    ApplyObsolete(ctx, proposal Proposal) (ApplyResult, error)
}

// DefaultApplyExecutor wires real memory.Store + policy + filesystem
type DefaultApplyExecutor struct {
    memStore    *memory.Store
    proposalStore *Store
    policy      policy.Engine  // from internal/policy/
    projectRoot string
    now         func() time.Time
}

func NewDefaultApplyExecutor(ms *memory.Store, ps *Store, p policy.Engine, root string, now func() time.Time) *DefaultApplyExecutor
```

### 2.4 `Store.Apply(ctx, proposalID, executor)` 公开方法

```go
// Apply runs the executor for the proposal and records the result.
// Refuses proposals that are not status=approved.
// Refuses proposals that already have a proposal_applies row (idempotent guard).
func (s *Store) Apply(ctx context.Context, proposalID string, executor ApplyExecutor) (ApplyResult, error) {
    // 1. Get proposal
    p, err := s.Get(ctx, proposalID)
    if err != nil { return ApplyResult{}, err }
    // 2. Refuse if not approved
    if p.Status != StatusApproved {
        return ApplyResult{}, fmt.Errorf("%w: proposal %s is %s, not approved", ErrProposalNotApplicable, proposalID, p.Status)
    }
    // 3. Idempotent guard: check if already applied
    if existing, _ := s.getApplyResult(ctx, proposalID); existing.AppliedAt != (time.Time{}) {
        return existing, nil  // already applied
    }
    // 4. Run executor
    result, err := executor.Apply(ctx, p)
    // 5. Record in proposal_applies
    if err := s.insertApplyResult(ctx, result); err != nil { ... }
    return result, nil
}
```

新增 sentinel：

```go
var (
    ErrProposalNotApplicable = errors.New("proposal is not applicable")  // status != approved
    ErrProposalAlreadyApplied = errors.New("proposal already applied")
)
```

### 2.5 memory.Store 升级 API

`internal/memory/store.go` 加 `UpgradeMemory(ctx, id, newClaim, newAuthority)`：

```go
func (s *Store) UpgradeMemory(ctx context.Context, id, newClaim string, newAuthority Authority) (Memory, error) {
    // 1. Get memory
    // 2. Validate: newAuthority must outrank current (rank < currentRank)
    // 3. Update claim + authority + updated_at
    // 4. Return updated memory
}
```

新增 sentinel `ErrMemoryAuthorityRegression`（不允许 downgrade）。

---

## 3. Apply 路径

### 3.1 `memory_upgrade` → `Store.UpgradeMemory`

```go
func (e *DefaultApplyExecutor) ApplyMemoryUpgrade(ctx, p Proposal) (ApplyResult, error) {
    // Body.Payload: { "memory_id": "mem_xxx", "new_claim": "...", "new_authority": "explicit" }
    memoryID := p.Body.Payload["memory_id"].(string)
    newClaim := p.Body.Payload["new_claim"].(string)
    newAuthority := memory.Authority(p.Body.Payload["new_authority"].(string))
    if newClaim == "" || newAuthority == "" {
        return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Result: "failed", Error: "missing memory_id/new_claim/new_authority"}, nil
    }
    updated, err := e.memStore.UpgradeMemory(ctx, memoryID, newClaim, newAuthority)
    if err != nil {
        return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: memoryID, Result: "failed", Error: err.Error()}, nil
    }
    return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: memoryID, Result: "success", AppliedAt: e.now()}, nil
}
```

### 3.2 `agents_md_revision` → 通过 Policy 写 AGENTS.md

```go
func (e *DefaultApplyExecutor) ApplyAgentsMdRevision(ctx, p Proposal) (ApplyResult, error) {
    // Body.Payload: { "section": "## X", "current": "...", "proposed": "..." }
    section := p.Body.Payload["section"].(string)
    proposed := p.Body.Payload["proposed"].(string)
    path := filepath.Join(e.projectRoot, "AGENTS.md")
    
    // Policy check
    if decision, err := e.policy.Authorize(ctx, policy.Request{
        Subject: p.ID, Action: "file.write", Target: path,
        Justification: fmt.Sprintf("Apply M4 proposal %s: revise %s", p.ID, section),
    }); err != nil || !decision.Allowed {
        return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: path, Result: "denied_by_policy", Error: "policy denied"}, nil
    }
    
    // 读 AGENTS.md，找 section，替换 proposed
    // 写 AGENTS.md via Patch Journal
    patchID, err := patchJournal.ApplyFileEdit(ctx, path, section, proposed)
    if err != nil { return ApplyResult{...Result: "failed"}, nil }
    
    return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: path, Result: "success", AppliedAt: e.now(), PatchID: patchID}, nil
}
```

### 3.3 `skill_draft` → 通过 Policy 写 skills/

```go
func (e *DefaultApplyExecutor) ApplySkillDraft(ctx, p Proposal) (ApplyResult, error) {
    // Body.Payload: { "skill_name": "...", "description": "...", "frontmatter": {...}, "body": "..." }
    skillName := p.Body.Payload["skill_name"].(string)
    body := p.Body.Payload["body"].(string)
    path := filepath.Join(e.projectRoot, "skills", skillName + ".md")
    
    // Policy check
    if decision, err := e.policy.Authorize(...); err != nil || !decision.Allowed {
        return ApplyResult{...Result: "denied_by_policy"}, nil
    }
    
    // 写新文件 via Patch Journal
    patchID, err := patchJournal.ApplyFileCreate(ctx, path, body)
    
    return ApplyResult{...Result: "success", PatchID: patchID}, nil
}
```

### 3.4 `obsolete` → `Store.Archive`

```go
func (e *DefaultApplyExecutor) ApplyObsolete(ctx, p Proposal) (ApplyResult, error) {
    memoryID := p.Body.Payload["memory_id"].(string)
    if err := e.memStore.Archive(ctx, memoryID); err != nil {
        return ApplyResult{...Result: "failed", Error: err.Error()}, nil
    }
    return ApplyResult{ProposalID: p.ID, Kind: p.Kind, Target: memoryID, Result: "success", AppliedAt: e.now()}, nil
}
```

> `Archive` 走 `Store.Forget(hard=false)` 路径（v0.1 已存在）；v0.2 改名为更明确的 `Archive` 即可。

---

## 4. CLI 设计

### 4.1 dispatcher 加 `case "apply"`

In `internal/app/reflect.go` 的 `dispatchReflect` switch，加：

```go
case "apply":
    return runReflectApply(ctx, args[1:], a, stdout, stderr)
```

> **关键**：和 Task 4 fix 一样，apply 是子动词而不是 flag。`reflect --since=7d` 不应该被路由到 `apply`。

### 4.2 `runReflectApply`

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
        pipeline.memoryStore, pipeline.proposalStore,
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
    if result.Result != "success" { return ExitRunError }
    return ExitOK
}
```

### 4.3 exit 码映射

`exitForStoreError` 加：

```go
case errors.Is(err, memoryproposal.ErrProposalNotApplicable):
    return ExitInvalidInput  // 2 - proposal 不是 approved
case errors.Is(err, memoryproposal.ErrProposalAlreadyApplied):
    return ExitInvalidInput  // 2 - idempotent
```

### 4.4 apply 后 user 反馈（v0.1 简化为 stdout）

```text
applied prop_xxx: kind=memory_upgrade target=mem_yyy result=success
applied prop_xxx: kind=agents_md_revision target=AGENTS.md result=success
applied prop_xxx: kind=obsolete target=mem_zzz result=denied_by_policy
  error: policy denied file.write on /AGENTS.md
```

---

## 5. Trust Set 增量场景

Append to `evals/memory/trust-set-v1.json` `tasks` array：

| ID | category | description | setup | actions | expected |
|---|---|---|---|---|---|
| `reflect-apply-memory-upgrade-success` | reflect | seed approved memory_upgrade proposal → apply → memory 升级 + status=success | `{seed_proposals: [approved memory_upgrade]}` | `[reflect_propose, reflect_approve, reflect_apply]` | `proposal_apply_result: success` |
| `reflect-apply-obsolete-success` | reflect | seed approved obsolete proposal → apply → memory 归档 | `{seed_proposals: [approved obsolete]}` | `[reflect_propose, reflect_approve, reflect_apply]` | `proposal_apply_result: success` |
| `reflect-apply-fails-not-approved` | reflect | seed proposed (not approved) → apply → 拒绝 | `{seed_proposals: [proposed]}` | `[reflect_propose, reflect_apply]` | `proposal_apply_result: failed, error: "not approved"` |
| `reflect-apply-already-applied` | reflect | seed approved + 已有 apply_log → apply → idempotent 返 existing | `{seed_proposals: [approved], seed_applies: [success]}` | `[reflect_propose, reflect_apply]` | `proposal_apply_result: success (existing)` |

4 新场景（45+5+4 = 54 总场景）。

Trust Set runner 扩：
- `Action.Type` 加 `"reflect_apply"` verb
- `setup.seed_applies` 新字段（runner 直接 INSERT proposal_applies row 模拟"已 apply"）
- `Expected.ApplyResult string` + `ApplyErrorContains string` 新断言字段

---

## 6. 不在本切片范围

- 自动 Apply（v0.3+）：arch §9.5 acceptance rate gate 之后才放开
- Apply 撤销 / Rollback（v0.3）
- 跨 Project apply（v0.2 限制为当前 projectRoot）
- Policy 系统重构（v0.2 复用既有）
- LLM-based Reflect 模式（独立切片）

---

## 7. 风险与回滚

| 风险 | 缓解 |
|---|---|
| Apply 改坏 AGENTS.md / Skill → 项目无法运行 | Policy 审批 + Patch Journal audit + v0.2 强制 policy `deny` 失败时 exit 1 + stderr 错误 |
| Apply 重复执行 | `proposal_applies` 表 UNIQUE 约束 + 启动时检查（Store.Apply idempotent guard） |
| `UpgradeMemory` authority 降级 | `ErrMemoryAuthorityRegression` sentinel；只允许 rank 减小（更权威） |
| Policy 没接入 → apply 无审批 | v0.2 强制 `policy.Engine` 非 nil；启动时 panic 检查 |
| proposals 表外键约束 | `proposal_applies.proposal_id` FK 引用 `reflection_proposals.id`；删除 proposals 时 ON DELETE CASCADE |

---

## 8. 验收标准（AC）

1. **`proposal_applies` migration 011** 通过；既有 008/009/010 链 load 测试不变
2. **`proposal_Store.Apply` happy path**：seed approved memory_upgrade → apply → memory 升级 + `proposal_applies` 1 row
3. **idempotent guard**：二次 apply 返 `ErrProposalAlreadyApplied`；或返 existing result（v0.2 决定见 §2.2）
4. **未 approved 的 apply 拒绝**：返 `ErrProposalNotApplicable` + exit 2
5. **memory upgrade authority regression 拒绝**：`UpgradeMemory("mem_x", "claim", AuthorityInferred)` 当前 explicit 时返 `ErrMemoryAuthorityRegression`
6. **Policy deny**：agents_md_revision / skill_draft 路径 policy deny 时返 `denied_by_policy`；不入 proposal_applies 失败条目
7. **CLI 5 子命令**：`reflect` / `reflect proposals` / `reflect approve` / `reflect reject` / `reflect apply`（slice 01 的 4 个 + 新 1 个）端到端
8. **Trust Set 54 场景**：50 旧 + 4 新全过；baseline 5 指标不退化
9. **本地质量门禁**：`gofmt -l .` 0 / `go vet ./...` clean / 4 目标 build OK / golangci-lint 0 / govulncheck no vulns
10. **docs**：`docs/development/phase-4-slice-02/IMPLEMENTATION_REPORT.md` 创建 + `README.md` M4 Slice 02 勾选

---

## 9. 文件清单（task 拆分前的预估）

### 新增

- `internal/session/migrations/011_proposal_applies.sql`
- `internal/memory/proposal/apply.go` (`ApplyExecutor` interface + `DefaultApplyExecutor`)
- `internal/memory/proposal/apply_test.go`
- `internal/app/reflect_apply.go` (`runReflectApply`) — 可选，合并到 `reflect.go`
- `internal/app/reflect_apply_test.go` — 可选
- `docs/development/phase-4-slice-02/IMPLEMENTATION_REPORT.md`

### 修改

- `internal/memory/store.go` — 加 `UpgradeMemory` + `ErrMemoryAuthorityRegression`
- `internal/memory/store_test.go` — 加 `TestUpgradeMemory` 系列
- `internal/memory/proposal/store.go` — 加 `Apply` 方法 + `ErrProposalNotApplicable` + `ErrProposalAlreadyApplied` + `getApplyResult` / `insertApplyResult` 内部 helpers
- `internal/memory/proposal/proposal.go` — 加 `ApplyResult` 类型 + `ApplyExecutor` interface
- `internal/memory/proposal/proposal_test.go` — 加 apply 相关测试
- `internal/app/reflect.go` — dispatcher 加 `case "apply"` + `runReflectApply` 函数
- `internal/app/reflect_test.go` — 加 1-2 个 apply happy-path 测试
- `internal/app/memory.go` — `exitForStoreError` 加 2 个新 sentinel 映射
- `internal/app/runtime.go` — `App.policy` 字段 + `openReflectPipeline` 暴露 policy
- `internal/memory/trustset/runner.go` — `Action.Type` 加 `reflect_apply` verb + `setup.seed_applies` + `Expected.ApplyResult` / `ApplyErrorContains` 断言
- `internal/memory/trustset/runner_test.go` — count 50 → 54
- `evals/memory/trust-set-v1.json` — 4 新场景
- `README.md` — M4 Slice 02 勾选

### 不改

- 008_memory.sql / 009_memory_source_command.sql / 010_reflection_proposals.sql
- `internal/memory/retrieve.go` scoreRecall
- `internal/agent/`
- go.mod / go.sum

---

## 10. 分段确认

请按 § 分段 review，每段 ack 或给修改建议：

- [ ] §1 背景与目标
- [ ] §2 数据模型（proposal_applies 表 + ApplyResult + Store.Apply）
- [ ] §3 4 个 apply 路径
- [ ] §4 CLI `reflect apply` 子命令
- [ ] §5 Trust Set 4 新场景
- [ ] §6 不在范围
- [ ] §7 风险
- [ ] §8 验收标准
- [ ] §9 文件清单

收到全部 ack 后转入 writing-plans 写实施计划。