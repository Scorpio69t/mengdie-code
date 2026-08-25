# M3 Slice 04 实施报告

> 状态：M3 Slice 04 全部 7 task 已完成，HEAD `d46309c`（Task 7 文档 commit 在其后），本地质量门禁全过，PR 待 user 审核后合并。
> 日期：2026-08-25
> Spec：`docs/superpowers/specs/2026-08-24-m3-slice-04-cross-authority-dispute-design.md`
> Plan：`docs/superpowers/plans/2026-08-24-m3-slice-04-cross-authority-dispute.md`
> Beads：`mengdie-5tw`（PR 后 close）

## 交付范围

M3 Slice 04 落地 spec §4.2 row 3（跨 Authority conflict 双方都置 disputed + `inferred` 不覆盖 `explicit`）+ slice 03 留下的 fingerprint auto-Approve 越权修复，并新增 CLI `memory conflicts` 子命令 + `memory why` Authority 等级差可视化。Trust Set 从 40 场景扩到 45 场景，新加 5 个 `cross-authority-*` / `auto-approve-skipped-cross-authority` / `auto-approve-still-runs-no-conflict` 场景覆盖安全修复；`extractor-hybrid-both` 旧场景按 spec §4.2 row 3 重写为双方都 `disputed`。

### 新增

- `internal/memory/memory.go` — `AuthorityRank(a Authority) int` 函数（与 `authorityWeight` map 并行，retrieval 公式不重构）
- `docs/development/phase-3-slice-04/IMPLEMENTATION_REPORT.md`（本文件）

### 修改

- `internal/memory/store.go` — dispute 循环移除 Authority 跳过；新增 `IsCrossAuthorityConflict(ctx, Memory) (bool, error)` 公开方法；package doc 同步更新
- `internal/agent/runtime.go` — `memoryWriter` interface 加第三方法 `IsCrossAuthorityConflict`；`applyMemoryExtraction` fingerprint 命中分支前加跨 Authority 守门（conflict → `continue`，不 Approve）
- `internal/memory/trustset/runner.go` — `extractAction` fingerprint 命中分支前加跨 Authority 守门（与 runtime 对称；外层 `stored.Status == StatusProposed` 守门已天然短路，显式 guard 是 spec §3.5 对称要求）
- `internal/app/memory.go` — 新增 `runMemoryConflicts` / `countConflictPeers` / `writeMemoryConflictsTable`；dispatcher 加 `case "conflicts":`；帮助文本扩 `conflicts`；`runMemoryWhy` 渲染 Authority rank + gap 行；修正 stale package doc（"9 CLI" → "10 CLI"，Task 6 reviewer follow-up）
- `internal/memory/memory_test.go` — `TestAuthorityRank`（6 case：explicit=1 / verified=2 / repository=3 / inferred=4 / unknown=MaxInt / empty=MaxInt）
- `internal/memory/store_test.go` — `TestStoreCrossAuthorityDispute`（3 rows seed：explicit+explicit+inferred；双方都翻 disputed；IsCrossAuthorityConflict 在两方向都 true，孤立 candidate 返 false）；`setupSeededStore` scope 拆分（4 authority → 4 scope_value）；`TestSupersedeMarksOldSuperseded` 改 self-contained pair
- `internal/memory/retrieve_test.go` — `setupSeededRetriever` scope 拆分；`TestTier1CatalogueFiltersStale` 改 per-scope aggregation
- `internal/app/agent_memory_test_helpers_test.go` — `setupMemoryStoreWithSeeds` scope 拆分（2 authority → 2 scope_value）
- `internal/agent/runtime_extractor_test.go` — `stubStore` 加 `conflictFn` 字段 + `IsCrossAuthorityConflict` 方法；新测试 `TestRunAppliesExtractionSkipsAutoApproveOnCrossAuthorityConflict`
- `internal/app/memory_test.go` — `TestMemoryConflictsList` + `TestMemoryWhyShowsAuthorityRankGap` + `TestMemoryWhyShowsAuthorityRankGapExplicitSide`（后者是 Task 3 fix 补的 explicit-side 回归测试）
- `internal/memory/trustset/runner_test.go` — 计数 40 → 45 + docstring 增 slice-04 scenario set 说明
- `evals/memory/trust-set-v1.json` — `extractor-hybrid-both` 重写（expected 双方 disputed）+ 5 新场景（45 总场景，distribution explicit 15 / repository 5 / verified 5 / inferred 20）
- `README.md` — M3 Slice 04 勾选

### 不改

- `internal/memory/retrieve.go` — `scoreRecall` 不动；`rank` 与 `weight` 并行
- `008_memory.sql` 与 `009_memory_source_command.sql` — 零 schema 变更
- `go.mod` / `go.sum`

## 关键设计与守则

- **`AuthorityRank` 是纯查询函数**：与 `authorityWeight` map 并行；retrieval 公式（`authorityWeight * 0.6 + cosineSimilarity * 0.3 + (1 - disputePenalty) * 0.1`）不重构。整数 rank 让「`inferred` 永远不覆盖 `explicit`」用 `<` 表达，不依赖浮点排序；未知 Authority 默认 `math.MaxInt`，未来加新 Authority 不静默越权。
- **`IsCrossAuthorityConflict` 对称语义**：用 `status != 'archived'` + `rank != ownRank`（对称）而非常规模板里的 `status = 'active'` + `rank < ownRank`（directional）。理由：dispute-marking 已把双方翻为 `disputed`，brief 的 `status='active'` filter 与 brief 测试自相矛盾（disputed peer 不可见）；对生产 caller（`applyMemoryExtraction` 上 `inferred`）行为等价——每个非 `inferred` peer 都 outrank `inferred`，所以对称形式与「更权威」严格形式 observationally 等价。详见 Task 2 report Deviations #1 + commit `34e2411` 的 docstring。
- **fingerprint auto-Approve 双重守门**：`runtime.go:640` 与 `trustset/runner.go:535` 同构；dispute-marking 已天然翻 candidate 到 `StatusDisputed`（外层 `stored.Status == StatusProposed` 短路），显式 `IsCrossAuthorityConflict` guard 是 spec §3.4 / §3.5 的对称要求与未来重构保护。Failure-silent：`auto_approve_conflict_check_failed`（DB error）`continue` 不 Approve；`auto_approve_skipped_cross_authority_dispute`（conflict hit）`continue`；绝不阻断 Run。
- **`memoryWriter` interface 最小化**：3 方法（`ProposeMemory` + `Approve` + `IsCrossAuthorityConflict`）；`*memory.Store` 隐式满足；production 装配（`internal/app/runtime.go:236` `MemoryStore: memoryStore,`）零改动。测试可注入 `stubStore` 录 call sequence，`conflictFn` 默认返 `false, nil` 保持 slice-03 既有行为。
- **Seeded fixture 拆分（Task 2.5）**：spec §4.2 row 3 强制后，4-authority 同 scope 不同 claim 会自然全部翻 `disputed`。`internal/memory/{store,retrieve}_test.go` 与 `internal/app/agent_memory_test_helpers_test.go` 把 4-authority seed 拆到 4 个 scope_value（`mengdie` / `mengdie-tools` / `mengdie-deps` / `mengdie-style`），避免 cross-authority dispute-marking 翻所有 seed 为 disputed。`TestSupersedeMarksOldSuperseded` 与 `TestTier1CatalogueFiltersStale` 改为自包含 / per-scope aggregation。
- **新 trustset 场景必带 `seed_events: [{"kind": "user.message"}]` + `{"type": "run_run"}`**：Trust Set runner 走 hybrid extractor，LLM.Extract 要求 `len(events) > 0` 否则不调 stub provider 无 candidate。`user.message` 是 `projectEventPayload` switch 留 0 的 kind，不触发任何 Rules pattern，不污染 cross-authority 断言；`run_run` 是把 session 状态推到 runner 可读位置。详见 Task 5 report Deviations #1。

## Trust Set 退出门禁（45 场景 baseline 5 指标）

实测（Task 5 commit `81e4377` 跑出，本 task 在 commit `d46309c` 后再次重跑确认）：

```bash
$ go test -race ./internal/memory/trustset -run TestRunnerProducesAllMetrics -count=1 -v
    runner_test.go:64: trust-set baseline: precision@5=0.33 false_recall=0.00 source_trace=0.98 auth_fid=1.00 why_complete=1.00
    runner_test.go:97: trust-set baseline: precision@5=0.33 false_recall=0.00 source_trace=0.98 auth_fid=1.00 why_complete=1.00 → evidence\memory-trust-v1.json
--- PASS: TestRunnerProducesAllMetrics (12.03s)
PASS
```

| Metric | Slice-03（40 场景） | Slice-04（45 场景） | 说明 |
|---|---|---|---|
| `precision@5` | 0.375 | 0.333 | 分子不变（15 explicit 命中），分母 40 → 45；5 新场景全部 `category="inferred"`，分母膨胀而非回归 |
| `false_recall_rate` | 0.000 | 0.000 | scripted path 无 false-recall 信号 |
| `source_traceability` | 0.975 | 0.978 | +1 inferred candidate 含 source.ref |
| `authority_fidelity` | 1.000 | 1.000 | guard 不误伤 `auto-approve-still-runs-no-conflict` 场景 |
| `why_completeness` | 1.000 | 1.000 | 不变 |
| `auto_approved_rate` | 0.80 | n/a（不重 track） | slice-03 IMPLEMENTATION_REPORT 已跟踪；runner 里没单独 metric 输出，slice-04 不重 track |

> 注：slice 04 baseline 5 指标全保持既有阈值。precision@5 0.375 → 0.333 是新 inferred 场景稀释分母，不是回归。`auto_approved_rate` 未在 runner 里计算，slice-04 不重 track；显式 scenario 设计已经覆盖 fingerprint auto-Approve 行为（`auto-approve-skipped-cross-authority` 走 `disputed`，`auto-approve-still-runs-no-conflict` 走 `auto-approved`）。

> Sort order 注：spec §5.1 要求 `memory conflicts --updated_at desc`，但实现走 `Store.List`（`evidence_score DESC, observed_at DESC`）。Dispute 行 `evidence_score = 0`，`observed_at` 与 `updated_at` 接近但不完全等价。属 brief/plan 层 gap，列入 follow-up。

## 验证

本机 Windows 主机实测结果（commit `d46309c` 后再跑一次确认；45 场景 Trust Set 由 gitignored evidence 重新落盘）：

```bash
gofmt -l .                                                            # 0 output
go vet ./...                                                          # clean
go test -race ./internal/memory -count=1                              # ok (7.284s)
go test -race ./internal/memory/extractor -count=1                    # ok (2.086s)
go test -race ./internal/memory/trustset -count=1                     # ok (14.601s; 45/45 scenarios)
go test -race ./internal/agent -count=1                               # ok (6.384s; 含 TestRunAppliesExtractionSkipsAutoApproveOnCrossAuthorityConflict)
go test -race ./internal/app -count=1                                 # ok (12.730s; 含 TestMemoryConflictsList + TestMemoryWhyShowsAuthorityRankGap{ExplicitSide})
go build ./cmd/...                                                    # OK (native)
CGO_ENABLED=0 GOOS=windows  GOARCH=amd64 go build ./cmd/...           # OK
CGO_ENABLED=0 GOOS=linux    GOARCH=amd64 go build ./cmd/...           # OK
CGO_ENABLED=0 GOOS=darwin   GOARCH=arm64 go build ./cmd/...           # OK (无 CGO 依赖；无须 Mac SDK)
CGO_ENABLED=0 GOOS=darwin   GOARCH=amd64 go build ./cmd/...           # OK
golangci-lint run ./...                                              # 0 issues
govulncheck@v1.1.4 ./...                                             # No vulnerabilities found
```

## Follow-up（v0.1 后续切片，不在本切片内）

继承 slice 01/02/03 follow-up（按 spec §7 / §8 顺序）：

- **撤回已 auto-Approved 但本应被新规则保护的 candidate**（spec §1.4 / §7）：本切片只阻止新增，旧的不回溯；未来需独立 Consolidate 流程（v0.2+ M4 切片）。
- **跨 scope ancestor 展开**（spec §4.2 row 4）：本切片用 `scope_kind + scope_value` 完全相同判定重叠，ancestor-scope 展开独立切片。
- **memory_evidence 加 `source=auto_approve` 列**（slice 03 IMPLEMENTATION_REPORT §Follow-up）：v0.2 evidence column 改动后 revisit `memoryStatusAliasFor` 二次过滤。
- **`memory supersede` 的跨 Authority 行为**：slice 01 §4.2 row 4 已部分定义，本切片不动。
- **`apiKeyRe` redaction 增强**：当前 `[A-Za-z0-9_-]{20,}`，未来若引入多段 token 模式需扩。
- **Live Provider LLM 路径冒烟**（slice 03 follow-up）：当前 live test 仅 Rules 端；如需 LLM half auto-Approve 全链路冒烟 + 跨 Authority 守门 LLM 段冒烟，需在 `MENGDIE_LIVE_SMOKE=1` + Provider env 的 CI schedule 跑。

Slice 04 新发现：

- **Task 2.5 fixture 迁移约定**：spec §4.2 row 3 强制后，seeded 测试 fixture 必须多 scope 而非多 authority。本切片已修 `internal/memory/{store,retrieve}_test.go` 与 `internal/app/agent_memory_test_helpers_test.go`，future 切片加新 test 必须遵循新 fixture 约定（`Scope{project, <unique>}` per row）。已在 Task 2.5 report §Files Touched 注明。
- **`Memory.UpdatedAt` sort order**（brief/plan gap）：`memory conflicts` 当前按 `evidence_score DESC, observed_at DESC` 排，与 spec §5.1 要求的 `updated_at desc` 不严格一致。Dispute 行 `evidence_score = 0`，实际等价。`ListQuery` 暂未暴露 `OrderBy` 字段；v0.2 可加。
- **`auto_approved_rate` runner metric**（slice 03 spec/brief 不一致 follow-up）：slice-03 跟踪的 6 指标实际只 5 个在 runner 里算（`auto_approved_rate` 由任务报告手算而非 runner 输出）；slice-04 不重 track。v0.2 可加 runner metric。
- **brief 偏差**：`runMemoryConflicts` 实现采用 struct embedding `memory.Memory` 替代 brief 的 re-declared fields（`type Row struct { memory.Memory; Peers int }`），保证 `memory conflicts --json` 是 `memory list --json` 的 strict superset，任何已消费 `memory list --json` 的下游工具无需变更。详见 Task 6 report Deviations #2。
- **Task 6 reviewer follow-up**：原 `memory.go` package doc 写「9 个 CLI」与当前实际 `conflicts` 子命令（10 个）不一致；本 task（Task 7）修正为「10 个 CLI」并把 `conflicts` 加入列表。

## 红线检查

- `liveprovider` evidence JSON 写盘前 API Key `bytes.Contains` 拒绝；M3 Slice 04 不改，沿用。
- 45 场景每个独立 Store（`t.TempDir()`）跑，跨场景不污染 dispute / Authority 守门（`runner.go:NewStore` per-scenario）。
- `applyMemoryExtraction` 两阶段（ProposeMemory → 可选 Approve）+ 跨 Authority 守门失败一律 warn + continue，绝不阻断 Run（spec §4.3）。`trustset/extractAction` 同样失败-silent（DB error `return fmt.Errorf` 由 runner task-level error handling 处理；guard conflict `continue`）。
- `memoryWriter` interface 仅 3 方法（`ProposeMemory` + `Approve` + `IsCrossAuthorityConflict`），production 装配零改动。
- `IsCrossAuthorityConflict` 失败（DB error）→ `continue`（不 Approve）；`auto-approve-skipped-cross-authority` Trust Set 场景守门回归覆盖。

## 关联 Beads

`mengdie-5tw`（M3 Slice 04）：7 个 task 全部 ship、45 Trust Set 场景全过（40 slice-03 + 5 slice-04）、`memory conflicts` CLI 子命令 + 等级差可视化 + fingerprint auto-Approve 跨 Authority 守门 + fixture 迁移 + 双守门 + 4 目标构建 + golangci-lint + govulncheck 全过、PR ready-for-review。`bd close` 待 user merge 后执行。
