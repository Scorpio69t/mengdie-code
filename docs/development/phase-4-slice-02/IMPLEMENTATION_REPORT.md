# M4 Slice 02 实施报告

> 状态：M4 Slice 02 全部 7 task 已完成，HEAD `18c1b17`（Task 7 文档 commit 在其后），本地质量门禁全过，PR 待 user 审核后合并。
> 日期：2026-08-26
> Spec：`docs/superpowers/specs/2026-08-26-m4-slice-02-apply-driver-design.md`
> Plan：`docs/superpowers/plans/2026-08-26-m4-slice-02-apply-driver.md`
> Beads：`mengdie-3d3`（PR 后 close）

## 交付范围

M4 Slice 02 落地 M4 Slice 01 follow-up #1：`reflect approve` 后的实际 apply 链路。`proposal_applies` 审计表 + `ApplyExecutor` interface + `mengdie reflect apply <id>` CLI 子命令 + 4 apply 路径（memory_upgrade / agents_md_revision / skill_draft / obsolete）+ Trust Set 4 新场景。

### 新增

- `internal/session/migrations/011_proposal_applies.sql` — apply 审计表 + 1 index（`applied_at DESC`）
- `internal/memory/proposal/apply.go` (290 lines) — `DefaultApplyExecutor` 4 路径实现 + 内联 `Engine` interface / `Request` struct
- `internal/memory/proposal/apply_test.go` (151 lines) — 2 happy-path 测试（memory_upgrade + obsolete）
- `docs/development/phase-4-slice-02/IMPLEMENTATION_REPORT.md`（本文件）

### 修改

- `internal/memory/proposal/proposal.go` — `ApplyResult` 结构 + `ApplyExecutor` interface（4 方法）+ `ApplyResultSuccess` / `ApplyResultFailed` / `ApplyResultDeniedByPolicy` 3 个 result 常量 + 2 sentinel（`ErrProposalNotApplicable` / `ErrProposalAlreadyApplied`）
- `internal/memory/proposal/store.go` — `Store.Apply` 公开方法 + `getApplyResult` / `insertApplyResult` helpers + `insertApplyResultSQL` 常量
- `internal/memory/proposal/proposal_test.go` — `mockApplyExecutor` mock + 3 apply store tests（approved / not-approved / idempotent）
- `internal/memory/store.go` — `UpgradeMemory` 公开方法 + `ErrMemoryAuthorityRegression` sentinel
- `internal/memory/store_test.go` — 3 upgrade tests（promote / regression / same-authority）
- `internal/app/reflect.go` — `dispatchReflect` 加 `case "apply"` + `runReflectApply` 函数
- `internal/app/reflect_test.go` — `openTestApplyStores` helper + `reflectApplyTestTime` 常量 + 2 apply tests（approved happy / not-approved reject）
- `internal/app/memory.go` — `exitForStoreError` 加 2 sentinel 映射（`ErrProposalNotApplicable` / `ErrProposalAlreadyApplied` → `ExitInvalidInput`）
- `internal/app/app.go` — `App.projectRoot` 字段（v0.2 文件写入路径的 join root）
- `internal/memory/trustset/runner.go` — `reflect_apply` action verb（`reflectApplyAction`）+ `seed_applies` seeding + `Expected.ApplyResult` / `ApplyErrorContains` 断言 + `insertApplyFailureRow` 失败记录 helper + `insertSeed` 加 `id` 字段 + `reflectProposeAction` 转发 `body_payload`
- `internal/memory/trustset/runner_test.go` — count 50 → 54 + docstring 增 slice-02 scenario set 说明
- `evals/memory/trust-set-v1.json` — 4 新场景（`reflect-apply-memory-upgrade-success` / `reflect-apply-obsolete-success` / `reflect-apply-fails-not-approved` / `reflect-apply-already-applied`）；distribution `{explicit: 15, repository: 5, verified: 5, inferred: 20, reflect: 9}`
- `internal/session/sqlite_store_test.go` — migration count 10 → 11 + 新 `TestOpenSQLiteAppliesProposalAppliesTable`
- `README.md` — M4 Slice 02 勾选

### 不改

- `008_memory.sql` / `009_memory_source_command.sql` / `010_reflection_proposals.sql` — 既有 migration 不动
- `internal/memory/retrieve.go` `scoreRecall` — 不重构
- `internal/policy/` — `Engine` interface 与既有 `policy.Engine` struct 命名冲突；本切片走 brief Option A fallback（内联在 `apply.go`），policy 包零改动
- `internal/agent/` — 不动
- `go.mod` / `go.sum`

## 关键设计与守则

- **Apply 走普通 Policy + Patch Journal 链路**（arch §9.4）：v0.2 简化：`memory_upgrade` / `obsolete` 直接调 memory Store；`agents_md_revision` / `skill_draft` 走 `os.WriteFile`（Patch Journal 三路合并留 v0.3，spec §6.2）
- **`proposal_applies.proposal_id UNIQUE` idempotent guard**：第二次 Apply 返 existing record 不调 executor（Task 2 spec §2.4 决定 v0.2 走「silent return existing」分支；`ErrProposalAlreadyApplied` 预留 wired by Task 5）
- **authority regression 拒绝**：`ErrMemoryAuthorityRegression`（rank ≥ current 拒升级，含 same-authority）；`Store.UpgradeMemory` 是 strictly promotional 入口；降级用 `Store.Forget` / `Store.Archive`
- **`Engine` interface + `Request` struct 内联在 `apply.go`**：避免 `internal/policy` 既有 `Engine` struct 命名冲突（Go forbids 同包重名 type）；v0.3 抽 shared package + 写 `policy.Authorizer` adapter shim
- **4 路径独立 `ApplyExecutor` 方法**：每个 proposal kind 一个方法；`Store.Apply` 按 `switch p.Kind` 路由；`ApplyResult.Target` 区分 memory_id / 文件路径
- **Policy gate 内联在文件系统路径**：`ApplyAgentsMdRevision` / `ApplySkillDraft` 第一个动作是 `e.policy.Authorize(ctx, Request{Action: "file.write", Target: ...})`；`denied` / `errored` 短路返 `ApplyResultDeniedByPolicy`（audit row 区分 veto vs runtime failure）
- **`os.WriteFile` 而非 Patch Journal**：v0.2 简化；`proposal_applies.patch_id` 列预埋但 4 路径当前都 NULL；v0.3 Patch Journal 三路合并替换
- **failure-silent Apply 走普通 Run error handling**：`Store.Apply` 失败不阻断 pipeline；runner 端 `reflectApplyAction` 检测 `applyErr != nil` 且 audit row 不存在时，写 `result=failed` 行（`insertApplyFailureRow`），不 scope creep 到 `proposal` 包
- **sibling `proposal.Store` 复用 sessionStore DB**：Task 5 CLI 在 `runReflectApply` 开 sibling store（与 `openReflectPipeline` 共享同一 `*sql.DB`）；避免改 Task 4 `pipeline.go` 加 accessor；零开销（`proposal.Open` 是 stateless wrapper）
- **`App.projectRoot` 字段在 `app.go`**：brief 写 `runtime.go` 但 `App` struct 实际在 `app.go`；v0.2 走 brief「policy 简化」分支（`nil` policy engine 配合 runtime resolver）
- **`proposalStore` 字段在 `DefaultApplyExecutor` 占位**：v0.2 4 路径不用 audit store；保留字段以便 v0.3 Patch Journal 集成时 constructor 不动

## Trust Set 退出门禁（54 场景 baseline 6 指标）

实测（Task 6 commit `18c1b17` 跑出，本 task 在 HEAD 再次重跑确认）：

```bash
$ go test -race ./internal/memory/trustset -run TestRunner -count=1 -v
    runner_test.go:87: trust-set baseline: precision@5=0.28 false_recall=0.00 source_trace=0.96 auth_fid=1.00 why_complete=1.00
    runner_test.go:120: trust-set baseline: precision@5=0.28 false_recall=0.00 source_trace=0.96 auth_fid=1.00 why_complete=1.00 → evidence\memory-trust-v1.json
--- PASS: TestRunnerProducesAllMetrics (15.11s)
PASS
ok      github.com/Scorpio69t/mengdie-code/internal/memory/trustset   17.726s
```

| metric | M4 Slice 01 (50) | M4 Slice 02 (54) |
|---|---|---|
| precision@5 | 0.30 | 0.28 |
| false_recall_rate | 0.000 | 0.000 |
| source_traceability | 0.98 | 0.96 |
| authority_fidelity | 1.000 | 1.000 |
| why_completeness | 1.000 | 1.000 |
| apply_success_rate (NEW v0.2) | n/a | 2/2 = 1.0（memory_upgrade + obsolete 全过） |

> 注：precision@5 0.30 → 0.28：4 新场景全 `category="reflect"`，拉高分母（50 → 54）但不增加 explicit 命中（仍 15）。属分母膨胀，非回归。source_traceability 0.98 → 0.96：4 新场景中 `reflect-apply-fails-not-approved` 走 `Store.Apply` not-approved 分支故意不写 audit row，`Observed.Source.Ref` fallback 没注入 `trustset:` 占位 → 拉低分母（runner 端 `insertApplyFailureRow` 写的是 `result=failed` 行，但 `Observed` 渲染链只看 `proposal_applies`，未在 `insertApplyFailureRow` 后回填）。v0.3 可加 `proposal_applies`-driven `Observed` 回填 fallback，本切片不修。
>
> 注：apply_success_rate（NEW v0.2）：runner 当前不在 baseline 输出行打印；上表是 task report 手算（`memory_upgrade` + `obsolete` 2 路径成功 / 4 总 apply 场景中 2 路径期望 success）。`fails-not-approved` + `already-applied` 不算 success。v0.3 加 runner metric。

## 验证

本地 Windows 主机实测（Task 6 commit `18c1b17` 之后、Task 7 commit 之前的 HEAD）：

```bash
gofmt -l .                                                                # 0 output
go vet ./...                                                              # clean
go build ./...                                                            # OK (native)
go test -race ./internal/memory -count=1                                 # ok (8.961s; memory + proposal + extractor + trustset 全过)
go test -race ./internal/memory/proposal -count=1                         # ok (4.461s; 17 tests: 5 detector + 2 pipeline + 4 store CRUD + 3 apply store + 2 apply executor + 1 sentinel)
go test -race ./internal/memory/trustset -run TestRunner -count=1 -v      # 54/54 PASS (17.726s; precision@5=0.28 false_recall=0.00 source_trace=0.96 auth_fid=1.00 why_complete=1.00)
go test -race ./internal/memory/extractor -count=1                        # ok (2.285s; 既有 M3 测试不破坏)
go test -race ./internal/agent -count=1                                   # ok (6.653s)
go test -race ./internal/app -count=1                                     # ok (16.616s; 含 9 TestReflect* 测试：7 slice-01 + 2 slice-02)
go test ./internal/app -run TestReflect -count=1 -v                       # 9/9 PASS (TestReflectApplyApprovedProposal + TestReflectApplyRejectsNotApproved)
CGO_ENABLED=0 GOOS=windows  GOARCH=amd64 go build ./cmd/...               # OK
CGO_ENABLED=0 GOOS=linux    GOARCH=amd64 go build ./cmd/...               # OK
CGO_ENABLED=0 GOOS=darwin   GOARCH=arm64 go build ./cmd/...               # OK（无 CGO 依赖；无须 Mac SDK）
CGO_ENABLED=0 GOOS=darwin   GOARCH=amd64 go build ./cmd/...               # OK
golangci-lint run ./...                                                   # 1 issue (errcheck: internal/memory/proposal/store.go:316; 见 Follow-up，沿用 slice 01)
go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...                     # No vulnerabilities found.
```

注：

- `internal/memory/trustset/evidence/memory-trust-v1.json` 与 `.beads/interactions.jsonl` 由 `TestRunnerProducesAllMetrics` 重新落盘（gitignored）；不入 commit
- CI step `运行 Memory Trust Set`（`go test -race ./internal/memory/... -count=1`）覆盖 proposal + trustset + extractor 三个子包 + memory 包；无需新增 step（apply 新文件 `internal/memory/proposal/apply.go` 已落在 `internal/memory/...` glob 内）
- `golangci-lint` 报 1 issue 是 slice 01 Task 1 引入的 pre-existing 问题（`store.go:316` `fmt.Fprintf(h, ...)` 写 hash.Hash 的 error 返回未检查），本切片 hard constraint 禁止 Go 代码改动，列入 Follow-up（继承 slice 01 §Follow-up）。`go vet` 与 `gofmt` 干净，不影响现有质量门禁
- `go run ./cmd/mengdie-eval` CLI 不接 `trust-set-v1.json`；Trust Set baseline 走 `go test -race ./internal/memory/trustset -run TestRunnerProducesAllMetrics -count=1`（slice 03 / slice 04 同约定）

## Follow-up（v0.2 后续切片，不在本切片内）

继承 M4 Slice 01 follow-up：

- **v0.3 Apply driver 完善**：Patch Journal 集成替换 `os.WriteFile`（spec §6.2 三路合并）；apply 撤销（rollback，`proposal_applies` 加 `reverted_at` + `reverted_by`）；跨 Project apply（当前 `projectRoot` 单值）
- **LLM-based Reflect**：5 patterns 当前 rule-based
- **Daemon / idle / cron**（arch §9.2 延期）
- **proposal_acceptance_rate runner metric + time_to_benefit**：slice 01 stub 0.0；v0.3 加 runner 输出
- **DetectCrossSessionPattern 收紧 scope/authority**：v0.1 简化（threshold 3）；v0.2 严格过滤
- **DetectRepeatedCorrection 收紧 session-scope**：v0.1 简化（无 session 边界）；v0.2 加 session 边界
- **insertStaleSeed raw SQL → Store.save StatusOverride**：v0.2 给 Store.save 加 `StatusOverride` 选项（同时支持 status=stale 持久化与 status=disputed 跨 authority 标记）
- **golangci-lint errcheck at `proposal/store.go:316`**：slice 01 引入 pre-existing，hard constraint 1 禁止本切片改 Go 代码，列入 v0.2 修复清单
- **`proposals_count_gte` 提升为 `*int`**：当前 int zero value 与「field absent」不可分
- **Reviewer trust chain**：`$USER` fallback `"mengdie"`；v0.2 接 `git config user.name` / `git config user.email`

M4 Slice 02 新增：

- **policy.Engine 抽到 shared package**（`internal/policy/`）：v0.2 临时内联在 `apply.go`（brief Option A fallback）；v0.3 重构成 `policy.Authorizer` adapter shim，避免 in-package 命名冲突
- **Apply 撤销 (rollback)**：v0.2 不可逆；`proposal_applies` 表加 `reverted_at` + `reverted_by` 字段 + rollback helper（spec §1.3 明确延期）
- **insertSeed 加 `id` 字段通用化**：v0.2 通过 `insertSeedWithID` raw SQL 实现；v0.3 runner `seedMemoriesFromSetup` 直接加 `id` 字段支持
- **多 patch_id chain**：`proposal_applies` 表当前 1 patch_id，复杂 multi-file 提案需 1:N 关联
- **`ApplyAgentsMdRevision` / `ApplySkillDraft` 测试覆盖**：Task 4 仅测 happy-path 内存路径；文件系统路径需 `t.TempDir` + project_root harness + stub `Engine`（Task 4 follow-up）
- **`reflect-apply-fails-not-approved` 跑后 Observed 回填**：runner `Observed.Source.Ref` fallback 在 `result=failed` 路径不打 `trustset:` 占位；v0.3 加 `proposal_applies`-driven Observed 回填以补 source_traceability
- **apply_success_rate runner metric**：当前 task report 手算；v0.3 runner 加 baseline 输出行
- **Run-time policy 接入**：v0.2 `App.policy` 字段未加，`runReflectApply` 走 nil policy（per brief Option A）；production caller（agent runtime）v0.3 接 `policy.Authorizer`

## 红线检查

- `liveprovider` evidence JSON 写盘前 API Key `bytes.Contains` 拒绝（M3 沿用；本切片不变）
- 54 Trust Set 场景每个独立 Store（`t.TempDir()`）跑，跨场景不互相污染；4 reflect action verbs 通过 `runnerState.lastProposalID` 局部 state 串接，新增 `reflect_apply` 走同 state；state 在 scenario 内 / 跨 scenario 不可见
- `Store.Apply` 失败一律不阻断 Run；失败条目仍记 `proposal_applies` 表（Task 6 runner `insertApplyFailureRow` 填 not-approved 缺口，scope 仅限 trustset 包）
- 4 apply 路径走普通 Policy + Patch Journal 链路（arch §9.4 安全边界保持）：`memory_upgrade` / `obsolete` 走 memory Store mutation；`agents_md_revision` / `skill_draft` 走 `os.WriteFile`（v0.2 简化，Patch Journal v0.3）；policy `denied_by_policy` 短路返 audit row `result=denied_by_policy` 不实际写盘
- `proposal` package 不反向 import `memory`（用 `*memory.Store` 是显式依赖，反向不是问题）；policy `Engine` 内联避免反向依赖 `internal/policy`
- `proposal_applies.proposal_id` UNIQUE + FK → `reflection_proposals(id)` ON DELETE CASCADE 保证 idempotent guard；migration 011 的 UNIQUE 索引让 `INSERT ... ON CONFLICT` 路径零开销
- `ErrMemoryAuthorityRegression` 拒绝任何 authority 降级（rank ≥ current 拒升级；同 authority 也拒）；降级走 `Store.Forget` / `Store.Archive`（arch §4.2 row 3）
- Apply 后 `ApplyResult.Target` 区分 memory_id / 文件路径 / patch_id；audit row 自描述，不依赖外部上下文
- `os.WriteFile` 路径 `projectRoot == ""` 显式拒绝，避免相对路径写 cwd（Task 4 守门）

## 关联 Beads

`mengdie-3d3`（PR merge 后 close）