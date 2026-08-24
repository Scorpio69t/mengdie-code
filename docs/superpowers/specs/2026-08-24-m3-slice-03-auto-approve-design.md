# M3 Slice 03 Design — ruleGoTest/ruleGoLint Schema Fix + Fingerprint Auto-Approve

> 状态：已通过 5 节分段确认，待 user 审核后转入 writing-plans。
> 日期：2026-08-24
> Beads：`mengdie-9xd`（M3 Slice 03）
> Spec 关联：M3 Slice 01 `docs/superpowers/specs/2026-08-24-m3-slice-01-trusted-memory-design.md`、M3 Slice 02 `docs/superpowers/specs/2026-08-24-m3-slice-02-extractor-design.md`

## 1. 背景

M3 Slice 02 实现了 `MemoryExtractor` 接口 + 3 个实现（Rules / LLM / Hybrid）+ `applyMemoryExtraction` 钩子 + `memory_recall` 工具注册表。**两个重要 follow-up 留到 slice 03：**

1. **`ruleGoTest` / `ruleGoLint` 在生产环境无法触发。** `internal/session/event_row.go` 的 `SourceRef` 投影只填 `events.ToolCompleted.Summary` 字段（`internal/session/sqlite_store.go:926`）。Agent runtime 写 `Summary: "完成"`（`internal/agent/runtime.go:864`），不包含 shell 命令文本。`ruleGoTest` / `ruleGoLint` 的 `strings.Contains(SourceRef, "go test")` / `strings.Contains(SourceRef, "golangci-lint")` 永远 match 不上真 session events，Trust Set 测试靠 hand-rolled fixture 才能让规则 fire。
2. **所有 LLM/rules 候选都需手动 Approve，浪费 fingerprint 类高确定性记忆的流程。** `Store.Approve` 公开存在（`internal/memory/store.go:818`），但 v0.1 hook 永远只调 `ProposeMemory` 不调 `Approve`。

## 2. 范围与不在范围

### 2.1 范围内

- `internal/session/migrations/009_memory_source_command.sql` — `ALTER TABLE events ADD COLUMN source_command TEXT`
- `internal/session/sqlite_store.go` — `Events` method 投影时优先 `p.SourceCommand`、fallback `p.Summary`
- `internal/agent/runtime.go` — shell tool 写 `ToolCompleted` 时把 `ShellArgs` argv 拼到 `source_command` JSON 字段
- `internal/memory/extractor/whitelist.go` — 8 个 fingerprint pattern 函数 + `splitForAutoApprove` 顶层函数
- `internal/agent/runtime.go` — `applyMemoryExtraction` 钩子分两阶段：先 `ProposeMemory` 所有候选，再对 fingerprint 命中的 `id` 调 `Store.Approve`；加 `autoApproved` 计数到 `RunResult`
- `internal/app/memory.go` — `list --status auto-approved` 新增过滤器（status=active AND has_evidence(source=auto_approve) query）
- `evals/memory/trust-set-v1.json` — 5 个新 `auto-approved` 场景
- `internal/memory/trustset/runner.go` — `expected.extracted_memories[].status` 支持 `auto-approved`（语义 = `status=active` + claim 匹配 fingerprint）
- `internal/memory/extractor/live_provider_test.go` — 加 1 个 auto-Approved live 场景
- `README.md` — 勾选 M3 Slice 03 + 新增 M3 Slice 04 占位
- `docs/development/phase-3-slice-03/IMPLEMENTATION_REPORT.md`

### 2.2 不在范围内

- Learning-based whitelist（v0.1 用硬编码 8 项 patterns；v0.2 可加 LLM-driven）
- 用户手动 Approve 高频 fingerprint 的"半自动"模式（v0.1 全自动；v0.2 加 env 开关）
- 跨 Authority dispute 标记（spec §4.2 row 3，仍 deferred 到 v0.1 follow-up）
- 嵌入向量 / Hybrid recall 升级（v0.2）
- M4 Reflect / Consolidate
- 完整 TUI（独立切片，按 `roadmap-full-tui` 走）

## 3. Schema 修复

### 3.1 Migration `009_memory_source_command.sql`

```sql
-- 009_memory_source_command.sql
-- 给 events 表加 source_command 列，让 ruleGoTest/ruleGoLint 在生产能触发。
ALTER TABLE events ADD COLUMN source_command TEXT;
```

Constraints:
- 纯 `ALTER TABLE ADD COLUMN`，不丢数据
- NULLABLE（已有历史 events 行的 `source_command = NULL`；event_row.go 投影时 fallback 到 `p.Summary`）
- 不改 go.mod / 不加新 Go 依赖

### 3.2 `EventRow` 投影更新

`internal/session/event_row.go` 投影 `Events` method 时：

```go
func (s *SQLiteStore) Events(ctx, sessionID string, limit int) ([]session.EventRow, error) {
    // existing SELECT now includes source_command
    for rows.Next() {
        var row session.EventRow
        var sourceCommand sql.NullString
        if err := rows.Scan(..., &sourceCommand); err != nil { ... }
        // 优先 source_command，回退到旧 summary
        if sourceCommand.Valid && sourceCommand.String != "" {
            row.SourceRef = sourceCommand.String
        } else {
            row.SourceRef = p.Summary
        }
    }
}
```

`event_row.go` struct 本身不变（`SourceRef string`），只是 `Events` method 内部的投影逻辑变了。

### 3.3 Agent runtime 写 `source_command`

`internal/agent/runtime.go` shell 工具执行成功路径（当前 line 864 附近）：

```go
// 现在（slice 02 状态）：
emitter.Emit(ctx, events.KindToolCompleted, events.ToolCompleted{
    CallID: call.ID, Tool: call.Name, Success: true, Summary: "完成", DurationMS: ...,
})

// 改成（slice 03）：
sourceCommand := strings.Join(shellArgs, " ")  // ["go", "test", "./..."] → "go test ./..."
emitter.Emit(ctx, events.KindToolCompleted, events.ToolCompleted{
    CallID: call.ID, Tool: call.Name, Success: true,
    Summary: "完成",
    SourceCommand: sourceCommand,  // 新增 JSON 字段
    DurationMS: ...,
})
```

`events.ToolCompleted` struct 加 `SourceCommand string `json:"source_command,omitempty"`` 字段（与 `Summary` 并列）。Slice 01 已有 `events.ToolCompleted.Summary string `json:"summary,omitempty"`` 风格一致。

## 4. fingerprint auto-Approve

### 4.1 `internal/memory/extractor/whitelist.go`（新文件）

```go
package extractor

// FingerprintPattern returns true if the given claim should be auto-approved
// without human review. v0.1 ships 8 patterns:
//   - 6 from the existing Rules (1:1 with the rules' Claim text)
//   - 2 additional fingerprint patterns the user already confirmed in
//     trust-set-v1.json (readme-Preference + stderr-Preference)
type FingerprintPattern func(claim string) bool

var fingerprintPatterns = []FingerprintPattern{
    isProjectUsesEditFile,
    isProjectUsesWriteFile,
    isProjectTestEntrance,
    isProjectUsesGolangciLint,
    isRunOverallSuccess,
    isProviderUnstable,
    isPrefersChineseREADME,
    isPrefersStderrFirst,
}

func isProjectUsesEditFile(c string) bool   { return strings.Contains(c, "edit_file") }
func isProjectUsesWriteFile(c string) bool  { return strings.Contains(c, "write_file") }
func isProjectTestEntrance(c string) bool    { return strings.Contains(c, "go test") }
func isProjectUsesGolangciLint(c string) bool { return strings.Contains(c, "golangci-lint") }
func isRunOverallSuccess(c string) bool     { return strings.Contains(c, "本次 Agent Run 整体成功") }
func isProviderUnstable(c string) bool      { return strings.Contains(c, "Provider 协议层不稳定") }
func isPrefersChineseREADME(c string) bool  { return strings.Contains(c, "中文 README") }
func isPrefersStderrFirst(c string) bool    { return strings.Contains(c, "stderr") }

// ShouldAutoApprove returns true if any fingerprint pattern matches the claim.
func ShouldAutoApprove(claim string) bool {
    for _, p := range fingerprintPatterns {
        if p(claim) { return true }
    }
    return false
}
```

### 4.2 Hybrid 流程不 auto-Approve（保持纯函数）

`Hybrid.Extract(ctx, sessionID)` 继续只产出 `[]Memory` 候选，不做 ProposeMemory/Approve。auto-Approve 决策放在上游 `applyMemoryExtraction` 钩子。

### 4.3 Agent 钩子分两阶段

`internal/agent/runtime.go` `applyMemoryExtraction` 改造：

```go
func (a *Agent) applyMemoryExtraction(ctx, request) {
    if a.memoryExtractor == nil || ... { return }
    candidates, _ := a.memoryExtractor.Extract(extCtx, request.RunID)
    if len(candidates) == 0 { return }
    if len(candidates) > 5 { candidates = candidates[:5] }

    var autoApproved, manual []memory.Memory
    for _, mem := range candidates {
        if ShouldAutoApprove(mem.Claim) { autoApproved = append(autoApproved, mem) }
        else { manual = append(manual, mem) }
    }
    for _, mem := range autoApproved {
        stored, err := a.memoryStore.ProposeMemory(extCtx, mem)
        if err != nil { a.warnExtraction(ctx, "auto_approve_propose_failed", err); continue }
        if err := a.memoryStore.Approve(extCtx, stored.ID); err != nil {
            a.warnExtraction(ctx, "auto_approve_approve_failed", err)
        }
        // emit kind=memory.extracted? status=auto-approved?
    }
    for _, mem := range manual {
        _, err := a.memoryStore.ProposeMemory(extCtx, mem)
        if err != nil { a.warnExtraction(ctx, "memory_extractor_propose_failed", err) }
    }
}
```

错误处理：propose 失败 → emit warning + 继续；approve 失败 → emit warning + 继续（不阻断 Run；状态保持 proposed，用户可手动 approve）。

`RunResult` 加 `AutoApprovedCount int` 字段（公开 JSON tag `auto_approved_count`）；`Run` 末尾 `return state.result(summary), nil` 前填 `result.AutoApprovedCount = len(autoApprovedProposeSucceededIDs)`。

## 5. CLI 扩展

`internal/app/memory.go` `list` 子命令：
- `--status` enum 扩 `proposed | active | stale | disputed | superseded | archived | auto-approved`
- `auto-approved` 是 status 过滤别名：翻译为 `status=active` + 后续加 `evidence.source=auto_approve` query（v0.1 简化为只查 `status=active`，因 v0.1 还没 production 写 evidence；v0.2 再加精确过滤）

`approve` 子命令行为不变（手动 Approve 仍可用）。

## 6. Trust Set 5 增量场景（`evals/memory/trust-set-v1.json`）

| ID | 期望产物 |
|---|---|
| `auto-approved-rules-edits` | 1 条 `项目使用 edit_file` 候选，**status=active**（auto-Approved，fingerprint 命中）|
| `auto-approved-rules-tests` | 1 条 `项目测试入口是 go test ./...` 候选，**status=active** |
| `auto-approved-rules-lint` | 1 条 `项目使用 golangci-lint` 候选，**status=active** |
| `auto-approved-llm-fingerprint` | LLM 抽 1 条 fingerprint claim（如 `项目偏好中文 README`），**status=active**（LLM 候选 + fingerprint 命中）|
| `auto-approved-llm-non-fingerprint` | LLM 抽 1 条 non-fingerprint claim（如 `用户偏好每次都跑 npm test`），**status=proposed**（不 auto-Approved）|

40 总场景（35 slice 02 + 5 slice 03）全过 + 6 指标 baseline。

## 7. 质量门禁（6 指标 baseline）

```
precision@5         = slice 02 baseline 不退化
false_recall_rate   = 0
source_traceability = 1
authority_fidelity  = 1
why_completeness    = 1
auto_approved_rate  = 1.0   (5/5 auto-approved 场景全过)
```

CLI 验证：`go test -race ./...`：除 pre-existing Windows shell test 外全 PASS。`go vet ./...` clean。`golangci-lint run ./...` 0 issue。`govulncheck@v1.1.4 ./...` No vulnerabilities。4 目标 `CGO_ENABLED=0 go build ./cmd/...` 全过。`go test -tags=liveprovider -run TestLiveProviderAutoApproveMemory ./internal/memory/extractor/` 有 env 时跑真 Provider。

## 8. 文件结构

新增：
- `internal/session/migrations/009_memory_source_command.sql`（1 行 ALTER TABLE）
- `internal/memory/extractor/whitelist.go`（8 个 fingerprint pattern + ShouldAutoApprove + 单元测试）
- `docs/development/phase-3-slice-03/IMPLEMENTATION_REPORT.md`

修改：
- `internal/session/sqlite_store.go`（`Events` method 投影时优先 `source_command`、fallback `Summary`）
- `internal/agent/runtime.go`（shell 工具写 `source_command` JSON 字段 + `applyMemoryExtraction` 分两阶段 + `RunResult.AutoApprovedCount`）
- `internal/app/memory.go`（`list --status auto-approved` 翻译）
- `internal/memory/trustset/runner.go`（`expected.extracted_memories[].status` 支持 `auto-approved` 语义）
- `evals/memory/trust-set-v1.json`（加 5 个 `auto-approved` 场景）
- `internal/memory/extractor/live_provider_test.go`（加 1 个 auto-Approved live 场景）
- `README.md`（勾选 M3 Slice 03 + 新增 M3 Slice 04 占位）
- `docs/superpowers/specs/2026-08-24-m3-slice-02-extractor-design.md`（必要的小更新：Hybrid 仍纯函数、auto-Approve 在 app 层）

## 9. 风险与回滚

| 风险 | 缓解 |
|---|---|
| migration 009 加列失败 → 旧数据 OK 但 `source_command=NULL`；ruleGoTest/ruleGoLint 仍依赖 fallback `Summary` | migration 是 ADD COLUMN（非 NOT NULL），SQLite 默认 NULL；安全回滚：drop column |
| fingerprint false-positive 触发 auto-Approve（错误 Approve 一条不该 Approve 的记忆） | 8 个 pattern 全是 spec 已认可的 fingerprint；v0.1 限定 fingerprint pattern 不动态增加；新增 pattern 需单元测试 + spec 更新 |
| auto-Approve 单条失败（ProposeMemory 或 Approve 异常）→ 记忆半 propose 状态不统一 | warn + continue，单条失败不阻断 Run；RunResult.AutoApprovedCount 准确反映成功数 |
| schema 修复后 `event_row.go` 投影需要 unit test 覆盖 NULL/non-NULL 两条路径 | test_event_row_test.go 新增 2 case（`source_command=NULL` 走 Summary fallback；`source_command=non-NULL` 走 source_command） |
| 真实 Provider 加新 case 路径可能让 Trust Set 35 → 40 场景部分失败 | fingerprint pattern 是 deterministic 字符串匹配，Provider 路径不影响；live_provider_test 1 个 auto-Approved 场景单独验 |

## 10. 不在范围内的明确条目

- Learning-based whitelist（v0.2+）
- 用户手动 Approve 高频 fingerprint 的"半自动"模式（v0.2+ 加 env 开关）
- 跨 Authority dispute 标记（spec §4.2 row 3，独立切片）
- 嵌入向量 / Hybrid recall 升级（v0.2+）
- M4 Reflect / Consolidate（独立切片）
- 完整 TUI（独立切片，按 `roadmap-full-tui` 走）
- slice 02 留的 `event_row.go` 全字段投影（v0.1 follow-up）
- 公共 `events.ToolCompleted` schema 的 `command` 字段独立化（v0.1 用 `source_command` 列替代；v0.2 可考虑专用 events 类型）

## 11. Follow-up Beads 候选

- 跨 Authority dispute 标记（spec §4.2 row 3）
- M4 Reflect / Consolidate
- 完整 TUI（按 `roadmap-full-tui` 持久记忆）
- event_row.go 全字段投影（v0.1 follow-up）
- Embedding-based memory recall（v0.2）
