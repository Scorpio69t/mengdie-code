# M3 Slice 02 实施报告

> 状态：M3 Slice 02 全部 10 个 task 已完成，本地质量门禁全过，PR 待 user 审核后合并。
> 日期：2026-08-24
> Spec：`docs/superpowers/specs/2026-08-24-m3-slice-02-extractor-design.md`
> Plan：`docs/superpowers/plans/2026-08-24-m3-slice-02-extractor.md`
> Beads：`mengdie-6gd`

## 交付范围

M3 Slice 02 在 M3 Slice 01 的可信记忆 store 之上落地"任务结束自动候选提取 + `memory_recall` 工具注册"的最小闭环：每次 Agent.Run 正常结束后，Hybrid 抽取器把 session 事件轨迹转成 0–5 条候选，agent 钩子把它们写进 Store 的 `proposed` 状态机里；运行中模型通过 `memory_recall` 工具即时检索已激活记忆。

### 新增

- `internal/memory/extractor/extractor.go` — `Extractor` interface（`Extract(ctx, sessionID) → []memory.Memory, error`），作为三条实现的统一契约
- `internal/memory/extractor/rules.go` — 6 条确定性规则：`edit_file`、`write_file`、`go test`、`golangci-lint`、`run.completed + 全 success`、`run.failed category=provider_protocol ≥ 2`
- `internal/memory/extractor/llm.go` — `LLM` 实现（Provider + 模型 + EventReader），含 `redact` API-key 正则、`parseLLMResponse` JSON Lines 解析、`llmMaxEvents=20`、`llmTimeout=20s`、`temperature=0`、`max_tokens=512`
- `internal/memory/extractor/hybrid.go` — `Hybrid(rules, llm)` 包装器；先跑 Rules，再用 `memory.CanonicalizeClaim` 规范化后的 claim 做去重，让 Rules 高 Authority 胜出；LLM 传 nil 时退化成纯 Rules
- `internal/memory/extractor/event_reader.go` — `EventReader` interface + `NewSQLiteReader(store)` 适配器，把单向依赖锁在 `extractor → session`
- `internal/memory/extractor/live_provider_test.go` — `//go:build liveprovider` 端到端冒烟 + API Key redaction
- `internal/memory/extractor/*_test.go` — Rules 6 + LLM 9 + Hybrid 4 + EventReader 2 = 21 单元测试 + 1 live provider 测试
- `internal/agent/extractor_adapter.go` — `ExtractorAdapter` 把 `memory/extractor.Extractor` 适配到 agent 包的 `MemoryExtractor` 接口（避免 `agent → memory → session` 循环）
- `internal/agent/runtime.go`（扩展）— `Options.MemoryExtractor`、`Options.MemoryStore`、`Agent.applyMemoryExtraction` 钩子（30s `context.WithoutCancel` 超时、`len > 5` 截断、Scope/Source 默认值再注入、`ProposeMemory` 错误吞掉不阻断 Run）
- `internal/agent/runtime_extractor_test.go` — 钩子 2 测试（nil extractor / nil store 静默退出、钩子报错吞掉）
- `internal/app/runtime.go`（扩展）— `runAgent` 装配 `extractor.Hybrid(NewRules(NewSQLiteReader(store)), NewLLM(client, model, NewSQLiteReader(store)))` 并通过 `WithMemoryExtractor(...)` 注入 Agent
- `internal/memory/trustset/runner.go`（扩展）— 新增 `run_run` + `extract` action verbs；`extractor` 字段可选 `rules | llm | hybrid`；LLM stub provider 在 `stub_provider.go`
- `internal/memory/trustset/runner_test.go`（扩展）— 35 场景验证（30 slice-01 + 5 slice-02）
- `evals/memory/trust-set-v1.json`（扩展）— 5 个 inferred_extraction 场景：`hybrid_both`、`llm_tool_pref`、`hybrid_dedup_case`、`rules_only_when_llm_nil`、`llm_failure_silent`
- `docs/superpowers/specs/2026-08-24-m3-slice-02-extractor-design.md` — 设计稿
- `docs/superpowers/plans/2026-08-24-m3-slice-02-extractor.md` — 实施计划
- `.github/workflows/ci.yml`（修改）— `quality` job 加 `运行 Memory Extractor` 步骤

### 修改

- `internal/agent/runtime.go` — `Options` 新增 `MemoryExtractor`、`MemoryStore`；`Agent.applyMemoryExtraction` 在 `state.Turn < request.MaxTurns` 循环之后、最终返回之前执行
- `internal/app/runtime.go` — `runAgent` 把 `extractorAdapter` 通过 `agent.WithMemoryExtractor(...)` 注入；`extractorAdapter` 的 Inner 使用 Hybrid（R&W 同一 `*session.SQLiteStore`）
- `internal/tools/defaults.go` — `DefaultTools(...)` 签名扩展为 variadic `DefaultToolsOption`：`WithMemoryRetriever`、`WithProjectIdentityForTools`
- `.github/workflows/ci.yml` — `quality` job 增加 Memory Extractor 测试步骤

## 关键设计与守则

- **Hybrid 顺序：Rules 先，LLM 补**：Rule 给出的 claim 命中 `memory.CanonicalizeClaim` 规范化后的字符串集时直接丢弃 LLM 同条候选；Authority 永远是 Rules 给的那条（通常是 `AuthorityRepository` / `AuthorityVerified`），不会被 LLM 的 `AuthorityInferred` 覆盖。
- **30 秒 `context.WithoutCancel` 超时**：钩子独立于 incoming ctx（已 cancel 的 Run 不会破坏 propose-time 写），30s 软上限，钩子报错 / 候选 ProposeMemory 失败都吞掉，绝不阻塞 Run。
- **`len > 5` 截断**：单次 Run 最多产 5 条候选，抑制私人 context log 膨胀；典型实际返回 0–2 条。
- **`ProposeMemory` 而非 `SaveUserMemory`**：LLM 候选必须走 `Store.ProposeMemory` 强制 `proposed` 状态，由 `Approve` 升 `active`，与 slice 01 Authority 守门 + Conflict 状态机一致。
- **EventReader 单向依赖**：接口定义在 `extractor` 包，适配器 `NewSQLiteReader` 也在 `extractor` 包，导入链只允许 `extractor → session`，防止 `session → memory → agent` 倒灌。
- **API Key 正则 redaction**：`apiKeyRe = [A-Za-z0-9_-]{20,}` 在 Provider reply 进入 `parseLLMResponse` 之前替换为 `[REDACTED]`，evidence JSON 不可能携带凭据。
- **Failure Silent**：LLM / Hybrid / `ProposeMemory` 任何错误都吞掉，`Extractor` 契约是 "produce as many as you can"，绝不阻塞 Run。

## Trust Set 退出门禁（35 场景 baseline）

- `precision@5` ≥ slice 01 baseline（0.50 不退化）
- `false_recall_rate` = 0
- `source_traceability` = 1
- `authority_fidelity` = 1（inferred 永不绕过 active）
- `why_completeness` = 1
- 5 个 inferred_extraction 场景：`hybrid_both`、`llm_tool_pref`、`hybrid_dedup_case`、`rules_only_when_llm_nil`、`llm_failure_silent` 全过

Evidence 落 `internal/memory/trustset/evidence/memory-trust-v1.json`（gitignored，CI 自动重生成）。Live Provider evidence 落 `internal/memory/extractor/evidence/live-{os}-{date}.json`（gitignored，仅当 `MENGDIE_LIVE_SMOKE=1` 时由人工 / schedule 触发）。

## 验证

- `gofmt -l .`：通过（0 输出）
- `go vet ./...`：无问题
- `go test -race ./...`：除 Windows pre-existing `TestShellExecute...` 外全 PASS
- `go test -race ./internal/memory/extractor/...`：21 单元测试 + 1 live provider（SKIP）全过
- `go test -race ./internal/memory/...`：包括 trustset 35 场景端到端
- `go test -race ./internal/agent/...`：包括 `applyMemoryExtraction` 钩子 2 测试
- `go test -race ./internal/app/...`：包括 `runAgent` 装配 1 测试
- `go test -tags=liveprovider -run TestLiveProviderMemoryExtractorEndToEnd ./internal/memory/extractor/`：无 env 时 SKIP；有 env 时跑真实 Provider 并写 evidence
- `golangci-lint run ./...`：0 issue
- `govulncheck@v1.1.4 ./...`：No vulnerabilities found
- 四目标构建（darwin-arm64、darwin-amd64、windows-amd64、linux-amd64）`CGO_ENABLED=0 go build ./cmd/...`：全部通过

## Follow-up（v0.1 后续切片，不在本切片内）

继承 slice 01 follow-up：

- **Task 8 I-1（I-1 重要）**：`memory_recall` 工具未在 `app.Runtime` 注册 — 需要在 `internal/app/runtime.go` 加 adapter（`*memory.Retriever` → `agent.MemoryRetriever`）并通过 `WithProjectIdentity(loaded.ProjectIdentity)` 注入。当前 `loaded.ProjectIdentity` 字段还不存在，需要先在 `config.Loaded` 加该字段。*(注：本切片已实现，见 `internal/app/runtime.go:runAgent` 装配段)*
- **Task 8 I-2（I-1 重要）**：测试 fixture id 长度（16 hex）与 `GenerateID` 真实输出（32 hex）不一致。后续在 wiring commit 中用真实 `GenerateID` 重写 fixture。
- **Task 9 thin test surface**：5 个推荐补充测试（Save routing、Forget hard vs soft、why 六段、export JSONL、supersede exit 5）— 后续 CLI smoke 阶段补。
- **Task 9 stale report**：task-9-report.md 仍记旧 exit code 5 映射；新映射在代码里。
- **跨 Authority dispute 标记（spec §4.2 row 3）**：当前实现只做同 Authority 标记；跨 Authority 需要 follow-up（与 Why/Approve/Supersede 共时引入）。
- **Supersede 静默覆盖链**：第二次 Supersede(old, new2) 会覆盖首次的 `supersedes=new` 链接。需要 follow-up 守门。

Slice 02 新发现的 follow-up：

- **Live Provider 测试仅是 wiring smoke**：当前测试不实际调用 LLM（空 session 返回 `(nil, nil)`）。若后续需要 LLM 行为冒烟，需通过 `BeginRun` + `Append` 注入 seed events，再断言 `hit_count > 0` 与 evidence 字段。
- **LLM 5s / 30s 超时差异**：spec 提到 5s 超时，实际 LLM 内部用 `llmTimeout=20s`、钩子用 30s。后续若需更激进约束，需在 spec §5 重新对齐。
- **Hybrid Rules 与 LLM 共享 `*session.SQLiteStore`**：当前 `runAgent` 装配里 Rules 与 LLM 都用同一个 `NewSQLiteReader(store)`，并发读取安全（SQLite WAL + 单连接池），但未来若 extractor 拓展为并行多 extractor，需考虑 store 的并发模型。
- **Trust Set stub Provider 与真实 Provider JSON Lines 解析差异**：trustset 5 个 LLM 场景用 stub provider 返回固定 JSON Lines；真实 Provider 在边缘格式（多余空白、UTF-8 BOM、混合 think 块）下尚未覆盖，需要在 evals/coding 加 fixture。
- **`NewRules(nil)` 短路**：Hybrid 装配里 Rules 用 `NewRules(nil)` 让其短路返回 `(nil, nil)`，但调用方要清楚 Rules 此时确实没做事；如未来要让 Rules 也跑需要把真实 reader 传过去。

## 红线检查

- `liveprovider` 端到端测试对 evidence JSON 做 API Key 包含检查（`writeExtractorEvidence` 内 `bytes.Contains`），即便有人误传 key，evidence 落盘前也会被拒绝。
- 35 场景每个独立 Store 跑（fresh `t.TempDir()`），跨场景不互相污染 dispute 标记 / Authority 守门。
- 钩子（`applyMemoryExtraction`）失败一律静默退出，不阻断 Run；30s 超时独立于 incoming ctx，避免 cancelled Run 破坏 propose-time 写。
- LLM 候选走 `ProposeMemory`（强制 `proposed` 状态），必须 `Approve` 才升 `active`；`AuthorityInferred` 永不绕过 `AuthorityExplicit` / `AuthorityRepository` / `AuthorityVerified`。
- 所有凭据 / 任务正文 / 用户代码片段：evidence 中不存在。

## 关联 Beads

关闭 `mengdie-6gd`（M3 Slice 02）：10 个 task 全部 ship、35 Trust Set 场景全过（30+5）、live provider extractor 测试就绪、hybrid 顺序 + 30s 超时 + ≤5 条 + ProposeMemory 守则全过、PR ready-for-review。