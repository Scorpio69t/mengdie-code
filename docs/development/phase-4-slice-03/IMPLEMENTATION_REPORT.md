# M4 Slice 03 实施报告

> 状态：M4 Slice 03 全部 3 task 已完成，HEAD 在 Task 3 commit（`feat(trustset): reflect_revert + 3 scenarios + docs (54 → 57)`）；本地质量门禁全过；PR 待 user 审核后合并。
> 日期：2026-08-27
> Spec：`docs/superpowers/specs/2026-08-27-m4-slice-03-revert-design.md`
> Plan：`docs/superpowers/plans/2026-08-27-m4-slice-03-revert.md`
> Beads：`mengdie-bpg`（PR 后 close）

## 交付范围

M4 Slice 03 落地 M4 Slice 02 follow-up #2：apply 撤销（audit-only）。`proposal_applies.reverted_at` + `reverted_by` 审计标记 + `Store.Revert` 公开方法 + `mengdie reflect revert <id>` CLI 子命令 + Trust Set 3 新场景（`reflect_revert` action verb）。

### 新增

- `docs/development/phase-4-slice-03/IMPLEMENTATION_REPORT.md`（本文件）

### 修改

- `internal/memory/trustset/runner.go` — `reflect_revert` action verb（`reflectRevertAction` handler）+ `insertApplySeed` 加 `reverted bool` 参数（迁移 012 `reverted_at` + `reverted_by` 列写入）+ `Expected.ProposalRevertResult` / `RevertErrorContains` / `ProposalRevertedSet` 3 新断言字段 + `assertExpected` 加 3 分支 + `hasProposalAssertion` 认识新字段 + dispatch switch 加 `case "reflect_revert"`
- `internal/memory/trustset/runner_test.go` — count 54 → 57 + docstring 增 slice-03 scenario set 说明
- `evals/memory/trust-set-v1.json` — 3 新场景（`reflect-revert-success` / `reflect-revert-fails-already-reverted` / `reflect-revert-fails-not-applied`）；distribution `{explicit: 15, repository: 5, verified: 5, inferred: 20, reflect: 12}`
- `README.md` — M4 Slice 03 勾选

### 不改

- `internal/memory/proposal/store.go` — `Store.Revert` / `ErrProposalNotApplied` / `ErrProposalAlreadyReverted` 已由 Task 1 落地；本切片零 proposal 包改动
- `internal/session/migrations/012_proposal_applies_revert.sql` — Task 1 已应用；不动
- `internal/app/reflect.go` — `runReflectRevert` / `case "revert"` 已由 Task 2 落地；本切片零 CLI 改动
- `internal/memory/proposal/apply.go` — `DefaultApplyExecutor` 4 路径不变
- `internal/policy/` — 不动
- `internal/agent/` — 不动
- `go.mod` / `go.sum`

## 关键设计与守则

- **Audit-only Revert**（arch §9.4）：v0.2 简化：`Store.Revert` 只 UPDATE `proposal_applies.reverted_at` + `reverted_by`，不实际 rewind memory row / AGENTS.md / archive / file（spec §1.3 明确延期到 v0.3 Patch Journal 集成）
- **`runnerState.lastProposalID` 复用**：`reflect_revert` 与 `reflect_apply` / `reflect_approve` / `reflect_reject` 共享同一 runner-local state，无需在 JSON action 里塞 `id` 字段
- **失败-不阻断 Run**：`reflectRevertAction` 吞 `Store.Revert` 返回的 error（`ErrProposalNotApplied` / `ErrProposalAlreadyReverted` 都是 Trust Set 期望的 failure mode），把 error 写到 `proposal_applies.error` 列让 assertExpected 有 row 可读
- **`insertApplySeed` 加 `reverted bool` 参数**：迁移 012 加了 `reverted_at` + `reverted_by` 两列，runner 的 raw-SQL 写入路径必须跟进；`reverted: true` 时写入固定 timestamp `"2026-08-27T00:00:00Z"`（不需要 clock detail，让 assertion 只检查 NULL vs non-NULL）
- **失败记录的 INSERT vs UPDATE 二选一**：`Store.Revert` 失败时 runner 先 `COUNT(*)` 探测 `proposal_applies` 是否已有 row：count==0 走 `insertApplyFailureRow`（not-applied 分支，无 row 可 UPDATE），count>0 走 `UPDATE proposal_applies SET error = ?`（already-reverted 分支，UNIQUE 阻止 INSERT）
- **`ProposalRevertResult` vs `ProposalRevertedSet` 双字段**：`ProposalRevertResult: "success"` 检 `reverted_at` 非 NULL；`ProposalRevertedSet: true/false` 是更显式的 bool 替代；`ProposalRevertResult: "failed"` 只是 label（实际检查靠 `RevertErrorContains`）
- **`RevertErrorContains` 读 `proposal_applies.error` 列**：runner 失败时把 `revertErr.Error()` 写进去；substring match 让 scenario author 可以 pin 具体 failure mode（"already reverted" / "has not been applied"）
- **3 新场景分布**：1 个 happy-path（seed_applies success → revert → reverted_at set）+ 1 个 already-reverted（seed_applies success+reverted=true → revert 短路 ErrProposalAlreadyReverted → error 列写 "already reverted"）+ 1 个 not-applied（无 seed_applies → revert 短路 ErrProposalNotApplied → runner INSERT result=failed 行）

## Trust Set 退出门禁（57 场景 baseline 6 指标）

实测（Task 3 commit HEAD 跑出）：

```bash
$ go test -race ./internal/memory/trustset -run TestRunner -count=1 -v
    runner_test.go:100: trust-set baseline: precision@5=0.26 false_recall=0.00 source_trace=0.96 auth_fid=1.00 why_complete=1.00
    runner_test.go:133: trust-set baseline: precision@5=0.26 false_recall=0.00 source_trace=0.96 auth_fid=1.00 why_complete=1.00 → evidence\memory-trust-v1.json
--- PASS: TestRunnerProducesAllMetrics (14.45s)
PASS
ok      github.com/Scorpio69t/mengdie-code/internal/memory/trustset   16.842s
```

| metric | M4 Slice 01 (50) | M4 Slice 02 (54) | M4 Slice 03 (57) |
|---|---|---|---|
| precision@5 | 0.30 | 0.28 | 0.26 |
| false_recall_rate | 0.000 | 0.000 | 0.000 |
| source_traceability | 0.98 | 0.96 | 0.96 |
| authority_fidelity | 1.000 | 1.000 | 1.000 |
| why_completeness | 1.000 | 1.000 | 1.000 |
| apply_success_rate (v0.2 stub) | n/a | 2/2 = 1.0 | 2/2 = 1.0 |
| revert_success_rate (NEW v0.2) | n/a | n/a | 1/1 = 1.0（reflect-revert-success 跑过；其他 2 故意 failed） |

> 注：precision@5 0.28 → 0.26：3 新场景全 `category="reflect"`，拉高分母（54 → 57）但不增加 explicit 命中（仍 15）。属分母膨胀，非回归。
>
> 注：revert_success_rate（NEW v0.2）：runner 当前不在 baseline 输出行打印；上表是 task report 手算（`reflect-revert-success` 1 路径成功 / 3 总 revert 场景中 1 路径期望 success）。`fails-already-reverted` + `fails-not-applied` 不算 success（design v0.2 audit-only 故意让它们走 ErrProposal*）。v0.3 加 runner metric。

## 验证

本地 Windows 主机实测（Task 3 commit HEAD）：

```bash
gofmt -l internal/memory/trustset/                                                # 0 output
go vet ./internal/memory/trustset/                                                # clean
go build ./...                                                                    # OK (native)
go test -race ./internal/memory/trustset -count=1 -v                              # 57/57 PASS (16.842s; precision@5=0.26 false_recall=0.00 source_trace=0.96 auth_fid=1.00 why_complete=1.00)
go test -race ./internal/memory -count=1                                           # ok (无回归)
go test -race ./internal/app -count=1                                             # ok (无回归；既有的 11 reflect tests + 2 Task 2 revert tests 全过)
node -e "const d=JSON.parse(...); console.log('tasks:', d.tasks.length)"          # tasks: 57
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/...                        # OK
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build ./cmd/...                        # OK
```

注：

- `internal/memory/trustset/evidence/memory-trust-v1.json` 与 `.beads/interactions.jsonl` 由 `TestRunnerProducesAllMetrics` 重新落盘（gitignored）；不入 commit
- CI step `运行 Memory Trust Set`（`go test -race ./internal/memory/... -count=1`）覆盖 trustset 包；runner 改动零 proposal 包/CLI 包改动，无需新增 step
- `go vet` 与 `gofmt` 干净，不影响现有质量门禁
- `go run ./cmd/mengdie-eval` CLI 不接 `trust-set-v1.json`；Trust Set baseline 走 `go test -race ./internal/memory/trustset -run TestRunnerProducesAllMetrics -count=1`（slice 03 同约定）

## Follow-up（v0.3 后续切片，不在本切片内）

继承 M4 Slice 02 follow-up：

- **v0.3 Patch Journal 集成替换 `os.WriteFile`**（spec §6.2 三路合并）；apply 实际 rollback（memory row rewind / AGENTS.md rewrite / archive restore / file write undo）；`proposal_applies` 加 `pre_apply_snapshot` 列（v0.2 加 `reverted_at` + `reverted_by`，v0.3 加 snapshot）
- **LLM-based Reflect**：5 patterns 当前 rule-based
- **Daemon / idle / cron**（arch §9.2 延期）
- **proposal_acceptance_rate runner metric + time_to_benefit**：slice 01 stub 0.0；v0.3 加 runner 输出
- **DetectCrossSessionPattern 收紧 scope/authority**：v0.1 简化（threshold 3）；v0.2 严格过滤
- **DetectRepeatedCorrection 收紧 session-scope**：v0.1 简化（无 session 边界）；v0.2 加 session 边界
- **insertStaleSeed raw SQL → Store.save StatusOverride**：v0.2 给 Store.save 加 `StatusOverride` 选项
- **golangci-lint errcheck at `proposal/store.go:316`**：slice 01 引入 pre-existing，hard constraint 1 禁止本切片改 Go 代码，列入 v0.3 修复清单
- **`proposals_count_gte` 提升为 `*int`**：当前 int zero value 与「field absent」不可分
- **Reviewer trust chain**：`$USER` fallback `"mengdie"`；v0.3 接 `git config user.name` / `git config user.email`

M4 Slice 03 新增：

- **Revert 实际 rollback（spec §1.3 延期项）**：v0.2 audit-only（只 UPDATE `reverted_at` / `reverted_by`），v0.3 加 `Store.Revert` 扩展接 `ApplyExecutor` 风格让 executor 走 4 kind 路径（memory_upgrade rewind / obsolete restore / agents_md rewrite / file write undo）— 与 `Store.Apply` + `ApplyExecutor` 同 shape
- **runner 输出行加 `revert_success_rate`**：当前 task report 手算；v0.3 runner baseline 输出行加 metric（与 slice 02 `apply_success_rate` 同期加）
- **`proposal_applies` 加 `pre_apply_snapshot` 列**：v0.3 让 rollback 读 pre-apply state；migration 013
- **ApplyExecutor rollback 方法**：4 kind 路径（ApplyMemoryUpgrade / ApplyObsolete / ApplyAgentsMdRevision / ApplySkillDraft）各加一个反向方法（RevertMemoryUpgrade / RestoreObsolete / ...）；`Store.Revert` 接 executor 后 dispatch by kind

## 红线检查

- `liveprovider` evidence JSON 写盘前 API Key `bytes.Contains` 拒绝（M3 沿用；本切片不变）
- 57 Trust Set 场景每个独立 Store（`t.TempDir()`）跑，跨场景不互相污染；3 reflect action verbs 通过 `runnerState.lastProposalID` 局部 state 串接，新增 `reflect_revert` 走同 state；state 在 scenario 内 / 跨 scenario 不可见
- `Store.Revert` 失败一律不阻断 Run；失败条目仍记 `proposal_applies` 表（runner 端失败时 INSERT result=failed 行（not-applied 分支）或 UPDATE 现有行的 error 列（already-reverted 分支），scope 仅限 trustset 包）
- `Store.Revert` v0.2 audit-only — 不 rewind memory row / AGENTS.md / archive / file（spec §1.3 明确延期到 v0.3）
- `proposal_applies.reverted_at` + `reverted_by` 迁移 012 已应用（Task 1）；本切片 runner 通过 `insertApplySeed` raw SQL 写入时同时填这两列（仅 `reverted: true` 时）
- `Store.Revert` 的 `RowsAffected == 0` 守门（Task 1 store.go:462）保证并发 Revert 不会双 stamp marker；CLI `ErrProposalAlreadyReverted` → `ExitInvalidInput` 映射（Task 2）保持
- `runnerState.lastProposalID` 仅 scenario 内可见，跨 scenario 不可见；runner `state` 在每个 `runOne` 调用新建

## 关联 Beads

`mengdie-bpg`（PR merge 后 close）
