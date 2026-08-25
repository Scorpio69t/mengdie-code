# M3 Slice 04 — 跨 Authority dispute 标记 设计

> 状态：草稿，待用户按 §分段确认后转入 writing-plans。
> 日期：2026-08-25
> 关联 Beads：（将创建 `mengdie-xxx`）
> 关联 spec：`docs/superpowers/specs/2026-08-24-m3-slice-01-trusted-memory-design.md` §4.2 row 3
> 关联架构：`ARCHITECTURE.md §8（可信记忆系统）`

---

## 1. 背景与问题

### 1.1 Slice 01 已规定但 v0.1 仅落地一半

`m3-slice-01-trusted-memory-design.md §4.2 row 3` 早就写明：

> claim 字符串不同 + scope 重叠 + **Authority 不同** → 双方都置 `disputed`，`inferred` 一方永远不覆盖 `explicit`；`memory why <id>` 额外展示「对方 claim + 双方 source + Authority 等级差」。

但当前实现（`internal/memory/store.go:288-297`）的 dispute 检测循环里有一行：

```go
if row.Authority != m.Authority {
    continue   // 跨 Authority 冲突被静默跳过
}
```

所以 v0.1.0 只做了同 Authority 冲突标记；跨 Authority 场景（典型：用户 `remember` 的显式事实 vs 模型推断 / 文件扫描出的同一议题不同表述）会**互相不知道对方存在**，召回时 ranking 0 又按 `authority_weight` 把低权一方排到底，结果是「错的显式被新错的覆盖，状态机永远不进 disputed」。

### 1.2 Slice 02/03 把这个问题持续 defer

- slice 02 IMPLEMENTATION_REPORT §Follow-up：「跨 Authority dispute 标记（spec §4.2 row 3）：当前实现只做同 Authority 标记；跨 Authority 需要 follow-up（与 Why/Approve/Supersede 共时引入）」
- slice 02 spec §15 follow-up Beads：`M3 Slice 04`
- slice 03 spec §10：「跨 Authority dispute 标记（spec §4.2 row 3，独立切片）」

Slice 03 的 fingerprint auto-Approve 又把这个风险放大了：一条 fingerprint 命中的 `inferred` 候选如果和一条已存在的 `explicit` 冲突，**会被自动 Approve 覆盖**显式记忆——直接违反 spec §4.2 row 3。

### 1.3 本切片目标

把 spec §4.2 row 3 真正落地，并补 Slice 03 留下的风险：

1. **跨 Authority 冲突检测**：scope 重叠 + claim 不同 + Authority 不同时双方都置 `disputed`
2. **Authority 等级差可视化**：`why` 输出新增 Authority rank 与等级差行
3. **fingerprint auto-Approve 不再越权**：候选若与已有高 Authority active 记忆冲突，跳过 auto-Approve（仍走 proposed）
4. **Trust Set 跨 Authority 场景**：3-5 个新场景覆盖 explicit↔inferred / verified↔inferred / repository↔inferred 三对组合
5. **CLI 可见性**：新增 `mengdie memory conflicts` 子命令列出所有 disputed 记忆及其冲突 peer

### 1.4 不在本切片范围

- 自动 Resolve 跨 Authority 冲突（v0.2+ 引入 Consolidate 流程，本切片只标记不解决）
- 跨 scope 冲突（spec §4.2 row 4 仍未到，独立切片）
- memory graph / memory_evidence schema 变更（v0.2 evidence source 列加后才会有彻底变化）
- 撤回已 auto-Approved 但本应被新规则保护的 candidate（仅阻止新增，旧的不回溯）

---

## 2. Authority Rank 模型（新增）

### 2.1 Rank 表

Authority 已经按权威等级映射到 `authorityWeight`（`internal/memory/retrieve.go:46-51`）：

| Authority | 现有 weight（用于 recall ranking） | 新增 rank 整数（用于比较） |
|---|---|---|
| `explicit` | 1.0 | 1 |
| `verified` | 0.8 | 2 |
| `repository` | 0.6 | 3 |
| `inferred` | 0.3 | 4 |

rank 越小越权威。整数让「`inferred` 永远不覆盖 `explicit`」这种比较可以直接用 `<` 表达，不依赖浮点排序。

### 2.2 在 `internal/memory/memory.go` 新增

```go
// AuthorityRank returns the rank integer for an Authority value.
// Lower is more authoritative. Used by cross-authority dispute detection
// (spec §4.2 row 3) and by fingerprint auto-Approve guard (slice 04 §3).
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

`authorityWeight` map 保留不变（retrieval 公式不重构）；`AuthorityRank` 是并行的纯查询函数。

### 2.3 Scope overlap 判断

复用 slice 01 已有的「scope 重叠」定义（`store.go:271-284` 的 `existing` 查询）：`scope_kind` + `scope_value` 完全相同即视为重叠。本切片不引入 ancestor-scope 展开（独立切片）。

---

## 3. 跨 Authority 冲突检测

### 3.1 改 `Store.save` 的 dispute 循环

`internal/memory/store.go:288-318` 现状：

```go
for _, row := range existing {
    if row.ID == m.ID { continue }
    if row.Authority != m.Authority { continue }   // ← 删
    if CanonicalizeClaim(row.Claim) == normalized { continue }
    disputeIDs = append(disputeIDs, row.ID)
}
```

修改后：

```go
for _, row := range existing {
    if row.ID == m.ID { continue }
    if CanonicalizeClaim(row.Claim) == normalized { continue }  // idempotent：同 claim 跳过
    // 跨 Authority 冲突标记：spec §4.2 row 3
    // 注：同 Authority 行为与 slice 01 完全一致；新增 cross-authority 分支
    disputeIDs = append(disputeIDs, row.ID)
}
```

### 3.2 关键语义保留（不变）

- 同 Authority 行为不变（slice 01 已测）
- 新行 status 仍按 §4.2 row 3「双方都置 disputed」由现有 `if len(disputeIDs) > 0 { m.Status = StatusDisputed }`（store.go:316-318）保证
- id 生成的 idempotency 路径不受影响（`store.go:266` 的 normalized 提前返回）

### 3.3 「`inferred` 不覆盖 `explicit`」的实质

v0.1.0 实现下「不覆盖」=「双方都 `disputed`，recall 时高 weight 一方排前」。本切片不改 retrieval 公式；新增的 `AuthorityRank` 用于：

- §3.4 的 auto-Approve 守门（rank 比较）
- §4 的 `why` 等级差展示（rank 比较）

### 3.4 fingerprint auto-Approve 越权守门（关键安全修复）

`internal/agent/runtime.go:applyMemoryExtraction`（slice 03 Task 4 引入）当前流程：

```go
stored, err := a.memoryStore.ProposeMemory(extCtx, mem)   // status=proposed
if extractor.ShouldAutoApprove(stored.Claim) {
    a.memoryStore.Approve(extCtx, stored.ID)              // 直接升 active
}
```

问题：如果 `stored.Claim` 规范化后与一条 `AuthorityRank < 4`（即非 inferred）记忆冲突，新 candidate 会跨越 §4.2 row 3 的「inferred 不覆盖 explicit」约束。

修复（仅 fingerprint 命中分支加守门）：

```go
if extractor.ShouldAutoApprove(stored.Claim) {
    if disputed, err := a.memoryStore.IsCrossAuthorityConflict(extCtx, stored); err != nil {
        a.warnExtraction(ctx, "auto_approve_conflict_check_failed", err)
        // 不阻断 Run，但也不 Approve
    } else if !disputed {
        if err := a.memoryStore.Approve(extCtx, stored.ID); err != nil { ... }
        autoApprovedCount++
    } else {
        a.warnExtraction(ctx, "auto_approve_skipped_cross_authority_dispute", nil)
        // 保持 status=proposed，让用户走 manual approve 流程
    }
}
```

新增 `Store.IsCrossAuthorityConflict(ctx, m) (bool, error)` 公开方法：

- 在 `memories` 表里查 `(scope_kind, scope_value) == m.Scope` 且 `CanonicalizeClaim(claim) != CanonicalizeClaim(m.Claim)` 且 `status='active'` 的同行
- 返回 `true` 当且仅当存在 rank < `AuthorityRank(m.Authority)` 的 active 行（即有更高权威记忆冲突）

`memoryWriter` interface（slice 03 Task 4 引入）需扩展第三方法 `IsCrossAuthorityConflict(ctx, Memory) (bool, error)`。`*memory.Store` 隐式满足（runtime_test.go 的 stub store 需补此方法的 stub 实现）。

### 3.5 Trust Set runner 对齐

`internal/memory/trustset/runner.go:routeExtractedCandidate`（slice 03 Task 5 已重构返回 `(memory.Memory, error)`）：

- `extractAction` 在 LLM 候选落地时也要查跨 Authority 冲突
- 若冲突且 LLM 候选是 `inferred`、peer 是 `explicit`/`verified`/`repository`，保持 `status=proposed`，不调 `memStore.Approve`
- Trust Set 场景里 `expected.extracted_memories[].status` 必须仍为 `"auto-approved"` 的场景 fingerprint candidate 必须不含 cross-authority conflict（否则应跑成 `proposed`）

---

## 4. `why` 输出新增 Authority 等级差

### 4.1 现状

`Store.why(ctx, id)` 输出的 `WhyReport.Conflicts` 是 `[]Memory`（store.go:122-123），每条 peer 仅有 `Memory` 字段。`memory why <id>` CLI 在 `app/memory.go:400-409` 渲染 `claim / authority / status` 等。

### 4.2 新增字段

`WhyReport.Conflicts` 元素类型不变（仍 `[]Memory`），但 CLI 渲染加 1 行 Authority rank：

```text
mem_xxx claim=... authority=explicit(rank 1) status=disputed source=...
mem_yyy claim=... authority=inferred(rank 4) status=disputed source=...
```

`rank` 数字来自新 `AuthorityRank` 函数。等级差一行紧跟：

```text
authority_rank_gap=3 (inferred > explicit by 3 ranks; explicit wins)
```

仅当 `len(Conflicts) > 0` 时打印。

### 4.3 Trust Set scenario 断言升级

`expected.extracted_memories[]` 单条增加可选字段 `"authority_rank_gte"` 与 `"authority_rank_lte"`，但 v0.1 简化：**先不引入 schema 字段**；Trust Set 通过 claim_contains + authority + status 三元组断言已能覆盖「跨 Authority 双方都 disputed」语义。Authority rank 验证留在单元测试（`TestAuthorityRank`）。

---

## 5. CLI：新增 `mengdie memory conflicts`

### 5.1 子命令

```
mengdie memory conflicts [--scope <kind>value] [--limit N]
```

无 positional arg。输出所有 `status='disputed'` 记忆，按 `updated_at desc` 排序，每行：

```text
id=<id> claim=<claim> authority=<auth>(rank N) status=disputed peers=<count> updated_at=<rfc3339>
```

`peers=N` 是 `WhyReport.Conflicts` 长度，即跨 Authority 冲突 peer 数（v0.1 简化：仅在跨 Authority 触发 dispute 时计 1，同 Authority 不计入；Trust Set 不强约束）。

### 5.2 复用 `runMemoryList` 框架

新增 `runMemoryConflicts(ctx, args, a, stdout, stderr) int`，复用 `a.newMemoryFlagSet`，flag 集：`-scope`、`-limit`、`-json`。`status='disputed'` 强制走死，不暴露给用户（用户用 `list --status disputed` 也能看到，但 `conflicts` 多打印 peers 列）。

`memoryAllowedStatuses` 不需要扩（disputed 已在）。

### 5.3 app/memory.go 调度 switch

`runMemory` dispatcher（app/memory.go:115-139）加 `case "conflicts":` 分支，与 list/show/why/... 平级。

---

## 6. Trust Set 新增场景

Append 到 `evals/memory/trust-set-v1.json` `tasks` array：

| ID | category | description | setup | expected |
|---|---|---|---|---|
| `cross-authority-explicit-vs-inferred` | explicit | 用户 `remember` 一条；随后 agent 提议 fingerprint 命中的冲突 claim | explicit active + LLM 候选 fingerprint match 但冲突 | 双方都 status=disputed；explicit 保持 priority（recall 时排前）；inferred 不进 active |
| `cross-authority-verified-vs-inferred` | verified | 一次成功 `go test ./...` 落 verified；随后 fingerprint 候选冲突 | verified active + LLM 指纹候选 | 双方 disputed；verified priority |
| `cross-authority-repository-vs-inferred` | repository | `edit_file` 成功 → repository fact；fingerprint 候选冲突 | repository active + LLM 候选 | 双方 disputed；repository priority |
| `auto-approve-skipped-cross-authority` | inferred | fingerprint 候选与 explicit active 冲突 | explicit active + 提议 | 候选保持 status=proposed，RunResult.AutoApprovedCount 不增 |
| `auto-approve-still-runs-no-conflict` | inferred | fingerprint 候选无冲突 | 无冲突 seed | 候选 auto-approved（回归测试，确保守门不误伤） |

5 个新场景，category 沿用现有规则（explicit 走 explicit；其余走 inferred 因 slice 03 设计）。distribution：explicit 16 / repository 5 / verified 5 / inferred 14（slice 03 后是 15/5/5/15；本切片 +1 explicit +5 inferred 减 1 inferred 因 1 旧 inferred 提到 explicit 分类？待 task 5 重新算）。**最简化**：新增 5 场景全部 `category: "inferred"`，与 slice 03 一致；distribution: explicit 15 / repository 5 / verified 5 / inferred 20。

### 6.1 旧场景回归

`extractor-hybrid-both` 等 slice 03 引入的 fingerprint 场景需要重新跑：fingerprint 命中但无跨 Authority 冲突时仍 auto-approved。trust-set baseline 6 指标继续 ≥ 阈值（precision@5, false_recall, source_trace, auth_fid, why_complete, auto_approved_rate）。

### 6.2 性能开销

每条 fingerprint 候选查一次 active peer 是 O(active_peers_in_scope) SQL，索引走 `scope_kind + scope_value + status` 复合 index（slice 01 已建）。单 Run 最多 5 候选 × 1 次查询 = 5 次额外 round-trip，可接受。

---

## 7. 不在本切片范围

- 自动 Consolidate / Resolve（v0.2+ M4 切片）
- 跨 scope ancestor 展开（独立切片）
- memory_evidence 加 `source=auto_approve` 列（slice 03 IMPLEMENTATION_REPORT §Follow-up 留的 v0.2 work）
- 撤回旧 auto-Approved 候选（仅阻止新增）
- `memory supersede` 的跨 Authority 行为（slice 01 §4.2 row 4 已部分定义，本切片不动）

---

## 8. 风险与回滚

| 风险 | 缓解 |
|---|---|
| `IsCrossAuthorityConflict` 加索引但性能下降 | 走 `idx_memories_scope_status`（slice 01 已建），单次查询 O(log N) |
| fingerprint auto-Approve 守门误伤合法场景（与高权威记忆「类似」但 canonicalize 后不同） | Trust Set 5 旧 fingerprint 场景（slice 03）必须仍 100% 通过；Task 5 加 1 regression 场景 `auto-approve-still-runs-no-conflict` |
| AuthorityRank 整数映射被未来新增 Authority 破坏 | 默认 `math.MaxInt`，新增 Authority 不会静默越权 |
| `why` 输出新增 rank 字段被下游消费者拒绝（JSON parse 失败） | rank 仅在 `len(Conflicts) > 0` 时出现，且作为字符串行渲染，不动 `Conflicts` 元素类型 |
| Trust Set 跑分下降 | 5 新场景预期增加 inferred 类场景分母，precision@5 可能微降（与 slice 03 退化原因同）；文档明确 |

---

## 9. 验收标准（AC）

1. **跨 Authority conflict 检测**：`TestStoreCrossAuthorityDispute` 单元测试通过；seed 一条 `AuthorityExplicit` active 记忆，调 `Store.SaveUserMemory`（authority=explicit）claim 不同 → 双方都 `status=disputed`；再调 `Store.ProposeMemory`（authority=inferred）相同 scope 不同 claim → 双方仍 disputed，新增候选 status=proposed 转 disputed
2. **fingerprint auto-Approve 守门**：`TestRunAppliesExtractionSkipsAutoApproveOnCrossAuthorityConflict` 通过；stub store 收到 `IsCrossAuthorityConflict=true` 时不调 `Approve`，`AutoApprovedCount=0`
3. **回归无破坏**：slice 01/02/03 全部测试（含 Trust Set 40 场景 + memory_writer 相关测试）通过
4. **`why` 等级差**：`TestWhyReportAuthorityRankGap` 通过；disputed 记忆的 `why` 输出含 `authority_rank_gap` 行
5. **CLI**：`mengdie memory conflicts` 列出所有 disputed 记忆，`peers` 列非 0
6. **Trust Set 45 场景**：5 新场景全过；40 旧场景不退化
7. **本地质量门禁**：`gofmt -l .` 空 / `go vet ./...` clean / 4 目标 build OK / golangci-lint 0 / govulncheck no vulns
8. **docs**：`docs/development/phase-3-slice-04/IMPLEMENTATION_REPORT.md` 创建；`spec/2026-08-24-m3-slice-01-trusted-memory-design.md` §4.2 不动（已规定）；`README.md` M3 Slice 04 勾选

---

## 10. 文件清单（task 拆分前的预估）

### 新增
- `docs/development/phase-3-slice-04/IMPLEMENTATION_REPORT.md`

### 修改
- `internal/memory/memory.go` — `AuthorityRank` 函数
- `internal/memory/store.go` — dispute 循环移除 Authority 跳过条件；新增 `Store.IsCrossAuthorityConflict` 公开方法
- `internal/agent/runtime.go` — `applyMemoryExtraction` 加守门；`memoryWriter` interface 加 `IsCrossAuthorityConflict` 方法
- `internal/agent/runtime_extractor_test.go` — 新测试 + stub store 补方法
- `internal/memory/why_test.go`（或 store_test.go）— `TestStoreCrossAuthorityDispute` + `TestWhyReportAuthorityRankGap`
- `internal/memory/trustset/runner.go` — `extractAction` 加跨 Authority 守门；`expectedMatches` 不变
- `internal/memory/trustset/runner_test.go` — 计数 40 → 45
- `internal/app/memory.go` — 新增 `runMemoryConflicts` + dispatcher case
- `internal/app/memory_test.go` — `TestMemoryConflictsList`
- `evals/memory/trust-set-v1.json` — 5 新场景
- `README.md` — M3 Slice 04 勾选

### 不改
- `internal/memory/retrieve.go` — `scoreRecall` 不动；高权威 + disputed 仍走 `authorityWeight` + dispute penalty 公式
- 008_memory.sql 与 009_memory_source_command.sql — 无新 schema
- go.mod / go.sum

---

## 11. 分段确认

请按 § 分段 review，每段 ack 或给修改建议：

- [ ] §1 背景与范围
- [ ] §2 Authority Rank 模型
- [ ] §3 跨 Authority 冲突检测 + auto-Approve 守门
- [ ] §4 `why` Authority 等级差输出
- [ ] §5 CLI `mengdie memory conflicts`
- [ ] §6 Trust Set 5 新场景
- [ ] §7 不在范围
- [ ] §8 风险
- [ ] §9 验收标准
- [ ] §10 文件清单

收到全部 ack 后转入 writing-plans 写实施计划。