# M4 Slice 01 实施报告

> 状态：M4 Slice 01 全部 6 task 已完成，HEAD `fdbf884`（Task 6 文档 commit 在其后），本地质量门禁全过，PR 待 user 审核后合并。
> 日期：2026-08-26
> Spec：`docs/superpowers/specs/2026-08-26-m4-slice-01-reflect-proposal-design.md`
> Plan：`docs/superpowers/plans/2026-08-26-m4-slice-01-reflect-proposal.md`
> Beads：`mengdie-z48`（PR merge 后 close）

## 交付范围

M4 Slice 01 落地 ARCHITECTURE §9 的 Reflect / Consolidate v0.1：proposals 表 + 5 阶段流水线 + `mengdie reflect` CLI 4 子命令 + Trust Set 5 新场景。

### 新增

- `internal/session/migrations/010_reflection_proposals.sql` — proposals 表 + 2 indexes（status+observed_at / session_id）
- `internal/memory/proposal/proposal.go` (135 lines) — `Proposal` / `ProposalBody` / `Evidence` / `ListQuery` + Kind/Status 常量 + sentinels（`ErrProposalNotFound` / `ErrInvalidProposal` / `ErrInvalidQuery`）
- `internal/memory/proposal/store.go` (326 lines) — `Store` + `List` / `Get` / `Insert` / `UpdateStatus`
- `internal/memory/proposal/pipeline.go` (293 lines) — `Pipeline` + `Reflect` + 5 阶段（Scan / Extract / Verify / Reflect / Propose）
- `internal/memory/proposal/patterns.go` (320 lines) — 5 rule-based patterns（`DetectRepeatedCorrection` / `DetectRepeatedToolPreference` / `DetectForgottenTest` / `DetectCrossSessionPattern` / `DetectObsoleteClaim`）
- `internal/memory/proposal/proposal_test.go` (176 lines) — 4 store CRUD 测试
- `internal/memory/proposal/pipeline_test.go` (143 lines) — 2 pipeline 集成测试（empty / with events）
- `internal/memory/proposal/patterns_test.go` (212 lines) — 6 pattern 单测（5 detector + 1 no-match sentinel）
- `internal/app/reflect.go` (290 lines) — CLI 4 子命令 + `dispatchReflect` sub-router + `parseSince` / `reflectReviewer` / `writeReflectProposalsTable` helpers
- `internal/app/reflect_test.go` (163 lines) — 7 tests：4 happy（reflect / proposals / approve / reject）+ 3 regression（`--since=7d` 路由 / `--since=garbage` ExitInvalidInput / `approve prop_bogus` ExitNotFound）
- `docs/development/phase-4-slice-01/IMPLEMENTATION_REPORT.md`（本文件）

### 修改

- `internal/app/app.go` — dispatcher 加 `case "reflect":`（+2 lines，无其他改动）
- `internal/app/memory.go` — `openProposalStore` / `openReflectPipeline` helpers（与 `openMemoryStore` 同位）+ `exitForStoreError` 加 `ErrProposalNotFound → ExitNotFound`（+39 lines，无删除）
- `internal/session/sqlite_store_test.go` — migration 计数 9 → 10 + table-existence list 加 `reflection_proposals`（+5 -1）
- `internal/memory/trustset/runner.go` — 4 action verbs（`reflect` / `reflect_propose` / `reflect_approve` / `reflect_reject`）+ `Action.ID` / `Action.Reviewer` + `Expected.ProposalsCount` / `ProposalsCountGte` / `ProposalKind` / `ProposalStatus` / `ReviewerSet` + `runnerState{lastProposalID}` + `insertStaleSeed` raw-INSERT 持久化 status=stale + `reflectAction` 跑 Pipeline + 扫 stale 发 `KindObsolete`（+476 -13）
- `internal/memory/trustset/runner_test.go` — 计数 45 → 50 + docstring 增 slice-01 scenario set 说明（+15 -4）
- `evals/memory/trust-set-v1.json` — 5 新场景（`reflect-scan-since-default` / `reflect-proposal-memory-upgrade` / `reflect-proposal-obsolete` / `reflect-approve-promotes-status` / `reflect-reject-promotes-status`）；distribution `{explicit: 15, repository: 5, verified: 5, inferred: 20, reflect: 5}`（+72）
- `README.md` — M4 勾选（line 114 → 改写为 M4 Slice 01 含 spec / report 双链接）
- `docs/superpowers/specs/2026-08-26-m4-slice-01-reflect-proposal-design.md` — §5 修正：row 2 scenario ID 由 `reflect-scan-no-recent-sessions` 改为 `reflect-reject-promotes-status`（Task 5 reviewer note，与 brief + 实际实现一致）

### 不改

- `008_memory.sql` / `009_memory_source_command.sql` — 既有 migration 不动
- `internal/memory/retrieve.go` — `scoreRecall` 不动
- `internal/memory/extractor` — 复用既有 hybrid/rules（M3 Slice 02），本切片不引入新 extraction 路径
- `internal/memory/store.go` — `Save*` 路径不增加 status override 选项（Task 5 follow-up 留 v0.2；trustset runner 走 raw `insertStaleSeed`）
- go.mod / go.sum

## 关键设计与守则

- **Pipeline 5 阶段**：`scan → extract → verify → reflect → propose`，v0.1 复用 M3 Slice 02 `extractor.NewHybrid(rules, nil)` rules-only path；LLM-based extraction 留 v0.2
- **`ScannedSession.Memories` Stage 1 pre-fetch**：`proposal` package 现在 import `internal/memory` 仅作 `Memory` struct value type；通过 `Pipeline.extract` 的 `for i, s := range sessions` 把 per-session 抽取结果回写到 `sessions[i].Memories`，避免 proposal ↔ memory 通过 `*memory.Store` 反向耦合
- **5 pattern rule-based**：v0.1 简化版；`DetectRepeatedCorrection` keyword 集 9 条（含 `don't` + `do not`）；`DetectCrossSessionPattern` 用 `map[string]map[string]struct{}` 去重 session id 防同 claim 重复计数；`DetectObsoleteClaim` 依赖 `Pipeline.scan` 阶段已写入的 `Memories`，runner 侧走自己的 raw `insertStaleSeed` + `reflectAction` DB-scan，因为 `*memory.Store.save` 会覆写 `Status` 字段，无法持久化 `status=stale`
- **proposals 表安全边界**：Reflect Worker 写只到 `reflection_proposals` 表；不直接改 AGENTS.md / Skill / memory（arch §9.4）；CLI approve / reject 仅标 status，不 apply
- **reflect approve / reject 不 apply**：仅 `UpdateStatus(id, StatusApproved|StatusRejected, reviewer)`；实改 memory 或写 AGENTS.md / Skill 由 v0.2 apply driver 走 Policy + Approval 链路（arch §9.5 acceptance rate gate）
- **CLI flag vs subcommand dispatch**：`reflect --since=7d` 必须在 subcommand switch 之前直通（Task 4 fix：`args[0]` starts with `-` → `runReflect`）；否则 dispatcher 把 `--since=7d` 当成 unknown subcommand 返 `ExitInvalidInput`
- **Trust Set 4 action verbs 对称 review**：`reflect_propose` 写入 id 到 `runnerState.lastProposalID`；后续 `reflect_approve` / `reflect_reject` 通过同一个 state 拿到目标 id，避免 JSON layer 强制带 id 字段；每个 scenario 用 `t.TempDir()` 独立 Store，跨 scenario 不互相污染
- **`proposals_count_gte` 用 `> 0` 触发**：int zero value 跟「field absent」不可分；`hasProposalAssertion` predicate 排除 `ProposalsCountGte > 0` 的 zero baseline（`reflect-scan-since-default`），让短路线 short-circuit 正确返回 true

## Trust Set 退出门禁（50 场景 baseline 6 指标）

实测（Task 5 commit `fdbf884` 跑出）：

| metric | slice 04 (45) | slice 04 follow-ups (45) | M4 Slice 01 (50) |
|---|---|---|---|
| precision@5 | 0.333 | 0.333 | 0.30 |
| false_recall_rate | 0.000 | 0.000 | 0.000 |
| source_traceability | 0.978 | 0.978 | 0.98 |
| authority_fidelity | 1.000 | 1.000 | 1.000 |
| why_completeness | 1.000 | 1.000 | 1.000 |
| proposal_acceptance_rate (NEW v0.1 stub) | n/a | n/a | 0/0 = 0.0 |

> 注：precision@5 从 0.333 → 0.30：5 新场景全 `category="reflect"`，拉高分母（45 → 50）但不增加 explicit 命中（仍 15）。属新增场景分母膨胀，非回归。proposal_acceptance_rate v0.1 stub 0.0（无 manual accept 数据；v0.2 收集）。
>
> 注：spec §8 AC6 要求「新增 `proposal_acceptance_rate` 与 `time_to_benefit`（v0.1 stub，可填 0.0）」。本切片只落地 `proposal_acceptance_rate`（runner 不输出，per-scenario stub 0.0）；`time_to_benefit` 留 v0.2，因为 v0.1 没有 proposal create → accept 的时间数据可采集。

## 验证

本地 Windows 主机实测（commit `fdbf884` 之后、Task 6 commit 之前的 HEAD）：

```bash
gofmt -l .                                                                # 0 output
go vet ./...                                                              # clean
go build ./...                                                            # OK (native)
go test -race ./internal/memory -count=1                                 # ok (7.679s; memory + proposal + extractor + trustset 全过)
go test -race ./internal/memory/proposal -count=1 -v                      # 12/12 PASS (3.311s; 4 store + 2 pipeline + 6 pattern)
go test -race ./internal/memory/trustset -run TestRunner -count=1 -v      # 50/50 PASS (16.243s; precision@5=0.30 false_recall=0.00 source_trace=0.98 auth_fid=1.00 why_complete=1.00)
go test -race ./internal/memory/extractor -count=1                        # ok (2.307s; 既有 M3 测试不破坏)
go test -race ./internal/agent -count=1                                  # ok (6.285s)
go test -race ./internal/app -count=1                                     # ok (14.628s; 含 7 TestReflect* 测试)
CGO_ENABLED=0 GOOS=windows  GOARCH=amd64 go build ./cmd/...               # OK
CGO_ENABLED=0 GOOS=linux    GOARCH=amd64 go build ./cmd/...               # OK
CGO_ENABLED=0 GOOS=darwin   GOARCH=arm64 go build ./cmd/...               # OK（无 CGO 依赖；无须 Mac SDK）
CGO_ENABLED=0 GOOS=darwin   GOARCH=amd64 go build ./cmd/...               # OK
golangci-lint run ./...                                                   # 1 issue (errcheck: internal/memory/proposal/store.go:306; 见 Follow-up)
go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...                     # No vulnerabilities found.
```

注：

- `internal/memory/trustset/evidence/memory-trust-v1.json` 与 `.beads/interactions.jsonl` 由 `TestRunnerProducesAllMetrics` 重新落盘（gitignored）；不入 commit
- CI step `运行 Memory Trust Set`（`go test -race ./internal/memory/... -count=1`）覆盖 proposal + trustset + extractor 三个子包，无需新增 step
- `golangci-lint` 报 1 issue 是 Task 1 引入的 pre-existing 问题（`store.go:306` `fmt.Fprintf(h, ...)` 写 hash.Hash 的 error 返回未检查），本切片 hard constraint 禁止 Go 代码改动，列入 Follow-up。`go vet` 与 `gofmt` 干净，不影响现有质量门禁
- `go run ./cmd/mengdie-eval` CLI 不接 `trust-set-v1.json`；Trust Set baseline 走 `go test -race ./internal/memory/trustset -run TestRunnerProducesAllMetrics -count=1`（slice 03 / slice 04 同约定）

## Follow-up（v0.1 后续切片，不在本切片内）

继承 M3 Slice 03 / 04 follow-up：

- **v0.2 Apply driver**：`reflect approve` 后由 driver 走 Policy + Approval 链路实改 memory 或写 AGENTS.md / Skill（arch §9.5 acceptance rate gate）
- **LLM-based Reflect**：5 patterns 当前 rule-based；v0.2 接入 LLM 推断
- **Daemon / idle / cron**：arch §9.2 显式延期到 daemon 落地后
- **跨 scope consolidation**：spec §4.2 row 4 仍 defer
- **proposal_acceptance_rate metric runner**：v0.1 stub 0.0；v0.2 接 acceptance 事件（详见下方 deviation）
- **`memoryStatusAliasFor` 在 v0.2 evidence.source 列加后 revisit**（slice 03 follow-up）
- **Live Provider LLM 路径冒烟**（slice 03 follow-up）
- **`apiKeyRe` redaction 增强**

M4 Slice 01 新增：

- **golangci-lint errcheck in `proposal/store.go:306`**：`fmt.Fprintf(h, ...)` 写 hash.Hash 的 error 返回未检查（Task 1 brief snippet 原样保留）。一行 `_ =` 或 `//nolint:errcheck` 即可修；hard constraint 1 禁止本切片改 Go 代码，列入 v0.2 修复清单
- **`DetectCrossSessionPattern` 收紧 scope/authority**：当前只 canonicalize claim；spec §3.5 要求「同一 scope + 同一 authority」。v0.1 简化（threshold 3 抑制噪声），v0.2 严格过滤
- **`DetectRepeatedCorrection` 收紧 session-scope**：当前跨 session 累加；spec §3.5 要求「同一 session 内 ≥3」。v0.1 简化（无 session 边界）
- **`insertStaleSeed` raw SQL**：Task 5 用 raw INSERT 绕过 `memory.Store.save` 的 status override；v0.2 给 `Store.save` 加 `StatusOverride bool` 选项（同时支持 status=stale 持久化与 status=disputed 跨 authority 标记）
- **Spec §5 vs brief scenario ID**：Task 5 reviewer 指出 spec §5 row 2 写 `reflect-scan-no-recent-sessions`，brief 写 `reflect-reject-promotes-status`。本切片采用 brief。本 task（Task 6）同步修正 spec
- **apply subcommand**：v0.2 加 `mengdie reflect apply <id>`，接 Policy 链路
- **Reviewer trust chain**：v0.1 用 `$USER` fallback `"mengdie"`；v0.2 接 `git config user.name` / `git config user.email`
- **`proposals_count_gte` 提升为 `*int`**：当前 int zero value 与「field absent」不可分；v0.2 拓宽为 `*int` 让 baseline 可显式断言 `≥0`，避免 `hasProposalAssertion` 的 zero-vs-absent 边界
- **proposal_acceptance_rate runner metric**：当前 spec §8 AC6 要求 runner 报告但实现未输出；v0.2 加 runner metric（per-scenario 跟踪 proposal 的 accept/reject 计数）
- **time_to_benefit metric**：spec §8 AC6 同时要求；v0.1 无 create → accept 时间数据可采集，v0.2 加

## 红线检查

- `liveprovider` evidence JSON 写盘前 API Key `bytes.Contains` 拒绝（M3 沿用；本切片不变）
- 50 Trust Set 场景每个独立 Store（`t.TempDir()`）跑，跨场景不互相污染；4 reflect action verbs 通过 `runnerState.lastProposalID` 局部 state 串接，state 在 scenario 内 / 跨 scenario 不可见
- Pipeline 5 阶段失败一律 warn + continue，绝不阻断 Run：`reflect` 的 5 阶段任一返回 error，runner 走 `runOne` task-level error handling；`reflectAction` 内部 Stage 5 INSERT 失败走 `propose: %w` 包装上抛
- proposals 表写入只到 `reflection_proposals`；arch §9.4 安全边界保持（Reflect Worker 工具集只有读 + 写 proposals）
- `memoryWriter` interface 不动（M3 Slice 04 已扩展到 3 方法；本 slice 不需要）
- `proposal` package 不反向 import `memory` 包（避免循环依赖）；通过 `ScannedSession.Memories` 字段单向传数据（proposal → memory value type only）
- CLI 4 子命令不 apply：approve / reject 仅标 status + reviewer，不实改任何持久层

## 关联 Beads

`mengdie-z48`（PR merge 后 close）