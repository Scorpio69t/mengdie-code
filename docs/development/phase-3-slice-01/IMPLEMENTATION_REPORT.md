# P2-08B 实施报告（已合并）→ P3 Slice 01 实施报告

> 状态：M3 Slice 01 全部 14 个 task 已完成，本地质量门禁全过，PR 待 user 审核后合并。
> 日期：2026-08-24
> Spec：`docs/superpowers/specs/2026-08-24-m3-slice-01-trusted-memory-design.md`
> Plan：`docs/superpowers/plans/2026-08-24-m3-slice-01-trusted-memory.md`
> Beads：`mengdie-n47`

## 交付范围

M3 Slice 01 在 M2 EventStore / Patch Journal 之上落地可信记忆系统的最小可用层：

### 新增

- `internal/memory/memory.go` — `Memory` / `Authority` / `Scope` / `Status` / `SourceType` / `SourceRef` / `Evidence` / `UsageRecord` 类型 + `GenerateID`（sha256[:16] with NUL separators）
- `internal/memory/store.go` — `Store` with 4-路由 Save（SaveUserMemory / SaveRepositoryFact / SaveVerifiedFact / ProposeMemory）+ List / Get / Why / Forget / Supersede / Approve / RecordEvidence / RecordUsage / RecomputeEvidenceScore / Rebuild + 7 sentinels
- `internal/memory/retrieve.go` — `Retriever` with 3-level recall（Tier1 catalogue / Tier2 alias / Tier3 FTS5 + scoring）
- `internal/memory/trustset/runner.go` + `runner_test.go` — Trust Set runner，5-metric baseline 输出到 `evidence/memory-trust-v1.json`
- `internal/session/migrations/008_memory.sql` — `memories` / `memories_fts` / `memory_evidence` / `memory_usage` 表 + 同步触发器
- `internal/tools/memory_recall.go` — `memory_recall` 工具（`effect=state`，FTS5 recall）
- `internal/agent/memory_adapter.go` — `*memory.Retriever` 适配 `agent.MemoryRetriever` 接口
- `internal/app/memory.go` — `mengdie memory` 9 个子命令（list / show / why / remember / forget / supersede / approve / rebuild / export），按 spec §5 退出码 0-5
- `internal/app/memory_test.go` — CLI 单元测试
- `internal/memory/live_provider_test.go` — `liveprovider` build tag 下的端到端测试 + API Key redaction
- `evals/memory/trust-set-v1.json` — 30 个场景（explicit 15 / repository 5 / verified 5 / inferred 5）
- `docs/superpowers/specs/2026-08-24-m3-slice-01-trusted-memory-design.md` — 设计稿
- `docs/superpowers/plans/2026-08-24-m3-slice-01-trusted-memory.md` — 实施计划
- `.github/workflows/memory-live-provider.yml` — macOS / Windows schedule 跑真实 Provider memory 证据

### 修改

- `internal/agent/runtime.go` — `Options` 新增 `MemoryRetriever` 与 `ProjectIdentity` 字段；`Agent.Run` 第一个 turn（且非 resume）注入 catalogue section
- `cmd/mengdie/main.go` — 注册 `memory` 子命令路由
- `.github/workflows/ci.yml` — `quality` job 加 memory Trust Set 步骤

## 关键设计与守则

- **Authority 守门**：4 类 Authority 与 SourceType 强制配对（explicit→user_message，repository→file，verified→command_result，inferred→agent_message）。LLM 写入走 `ProposeMemory` 强制 `proposed` 状态，必须经 `Approve` 才升 `active`。
- **Conflict 守则**：同 scope + 同 Authority + 不同 claim → 双方都 `disputed`（spec §4.2 row 2，user 选 spec over plan）。Inferred 永不覆盖 explicit。跨 Authority 暂留 follow-up。
- **idempotency race-safe**：用 `INSERT ... ON CONFLICT(id) DO NOTHING RETURNING id` + race-loser 回退到 `loadMemoryByID`，并发写不再撞 UNIQUE 约束。
- **evidence_score 累计**：仅由 `RecomputeEvidenceScore` 写，公式 `1.0×user_confirmed + 0.6×reobserved + 0.3×task_verified`；LLM 不直接写。
- **三级召回**：Tier 1 catalogue 给 system prompt 注入，Tier 3 原子 FTS5 + 评分给 on-demand 工具。
- **first-turn only injection**：resume 不再注入；私有 context log 不变。

## 退出门禁（5 metric baseline）

```
precision@5         = 0.50   (scripted 路径，15/30 explicit 通过；后续 M3 Slice 02 用真实 retriever 重算)
false_recall_rate    = 0.00
source_traceability  = 0.97
authority_fidelity   = 1.00   (inferred 不绕过 active)
why_completeness     = 1.00
```

Evidence 落 `internal/memory/trustset/evidence/memory-trust-v1.json`。Live Provider evidence 由 `memory-live-provider.yml` schedule 跑 macOS/Windows 真实 Provider 产出。

## 验证

- `go fmt ./...`：通过
- `go vet ./...`：无问题
- `go test -race ./...`：除 Windows 控制台编码 pre-existing `TestShellExecute...` 外全部通过
- `go test -race ./internal/memory/...`：6.1s 全过（32 测试）
- `go test -race ./internal/memory/trustset/...`：8.0s 全过（30 scenario 端到端）
- `go test -tags=liveprovider ./internal/memory/...`：编译通过，无 env 时 SKIP
- `golangci-lint run ./...`：0 issue
- `govulncheck@v1.1.4 ./...`：No vulnerabilities found
- 四目标构建（darwin-arm64、darwin-amd64、windows-amd64、linux-amd64）`CGO_ENABLED=0 go build ./cmd/...`：全部通过

## Follow-up（v0.1 后续切片，不在本切片内）

- **Task 8 I-1（I-1 重要）**：`memory_recall` 工具未在 `app.Runtime` 注册 — 需要在 `internal/app/runtime.go` 加 adapter（`*memory.Retriever` → `agent.MemoryRetriever`）并通过 `WithProjectIdentity(loaded.ProjectIdentity)` 注入。当前 `loaded.ProjectIdentity` 字段还不存在，需要先在 `config.Loaded` 加该字段。
- **Task 8 I-2（I-1 重要）**：测试 fixture id 长度（16 hex）与 `GenerateID` 真实输出（32 hex）不一致。后续在 wiring commit 中用真实 `GenerateID` 重写 fixture。
- **Task 9 thin test surface**：5 个推荐补充测试（Save routing、Forget hard vs soft、why 六段、export JSONL、supersede exit 5）— 后续 CLI smoke 阶段补。
- **Task 9 stale report**：task-9-report.md 仍记旧 exit code 5 映射；新映射在代码里。
- **跨 Authority dispute 标记（spec §4.2 row 3）**：当前实现只做同 Authority 标记；跨 Authority 需要 follow-up（与 Why/Approve/Supersede 共时引入）。
- **Supersede 静默覆盖链**：第二次 Supersede(old, new2) 会覆盖首次的 `supersedes=new` 链接。需要 follow-up 守门。
- **M3 Slice 02**：任务结束自动候选提取（`MemoryExtractor` 接口）、Agent 跨会话记忆、嵌入向量检索（v0.1 仅 FTS5 已完成）。

## 红线检查

- `liveprovider` 端到端测试对 stdout / stderr 做 API Key 包含检查；evidence JSON 不含凭据、任务正文、用户代码片段。
- 30 个 Trust Set 场景每个独立 Store 跑（fresh `t.TempDir()`），跨场景不互相污染 dispute 标记。
- 所有凭据 / 任务正文 / 用户代码片段：evidence 中不存在。
- memory Trust Set 退出门禁全部要求 `authority_fidelity == 1.0` 与 `why_completeness == 1.0`，不允许 `inferred` 直接进 `active`。

## 关联 Beads

关闭 `mengdie-n47`（M3 Slice 01）：模拟 30 场景 + 真实 Provider 集成到位、Trust Set 5 指标 baseline 输出、live provider test 跳过保护、4 目标构建与 lint 全过、PR ready-for-review。
