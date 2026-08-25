# M3 Slice 03 Implementation Plan — ruleGoTest Schema Fix + Fingerprint Auto-Approve

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land `009_memory_source_command.sql` migration + EventRow projection update + agent shell-tool writes `source_command` + `internal/memory/extractor/whitelist.go` 8 fingerprint patterns + Agent `applyMemoryExtraction` hook split into two phases (ProposeMemory then Approve for fingerprint matches) + CLI `--status auto-approved` filter + 5 Trust Set `auto-approved` scenarios + 1 live-provider auto-Approved scenario, so `ruleGoTest`/`ruleGoLint` fire in production and 5 new scenarios validate auto-Approve vs proposed routing.

**Architecture:** Hybrid.Extract stays pure (returns `[]Memory`). New `extractor.SplitForAutoApprove(candidates)` split into `autoApproved`/`manual`; `applyMemoryExtraction` calls `ProposeMemory` on each, then `Store.Approve(id)` on the fingerprint-matched ones. EventRow projection prefers `events.source_command` (new column) over `events.ToolCompleted.Summary`; agent shell-tool writes the actual command to `source_command` at emit time.

**Tech Stack:** Go 1.26.6, `modernc.org/sqlite` (existing), existing `internal/session` migration chain, existing `internal/memory/extractor` skeleton.

## Global Constraints

- Go 1.26.6，module `github.com/Scorpio69t/mengdie-code`
- 仅 `CGO_ENABLED=0` 跨平台构建；四目标必须通过：darwin-arm64、darwin-amd64、windows-amd64、linux-amd64
- 禁止在用户仓库中自动 git commit / push
- 任何 `git commit` 由执行人显式触发
- migration `009_*` 是 `ALTER TABLE events ADD COLUMN source_command TEXT`（nullable，零数据丢失）
- 不修改既有 `008_memory.sql` 与既有 `EventRow` struct
- `extractor.Hybrid` 仍纯函数（只返回 `[]Memory`，不调 `Store`）
- fingerprint patterns 是包级 `var` 静态 list，零配置驱动
- 单条 `ProposeMemory` / `Approve` 失败 emit warning + 继续，不阻断 Run
- `RunResult.AutoApprovedCount int` 公开 JSON tag `auto_approved_count`
- 中文优先 package doc + 英文 inline comments（与项目约定一致）
- 错误统一用 `errors.New(...)` + `fmt.Errorf("%w", sentinel)`
- 任何 fingerprint false-positive 触发 auto-Approve 是 spec §11 follow-up 关注点
- Live provider test `//go:build liveprovider`，env 缺失 SKIP

---

## File Structure

### 新增
- `internal/session/migrations/009_memory_source_command.sql` (1 行 ALTER TABLE)
- `internal/memory/extractor/whitelist.go` (8 fingerprint pattern + `ShouldAutoApprove` + 单元测试)
- `internal/memory/extractor/whitelist_test.go` (8 个 pattern 各 1 case + 1 集成 case)
- `docs/development/phase-3-slice-03/IMPLEMENTATION_REPORT.md`

### 修改
- `internal/session/sqlite_store.go` — `Events` method 投影优先 `p.SourceCommand`、fallback `p.Summary`
- `internal/agent/runtime.go` — shell 工具写 `ToolCompleted.SourceCommand`；`applyMemoryExtraction` 分两阶段；`RunResult.AutoApprovedCount`
- `internal/app/memory.go` — `list --status auto-approved` 翻译
- `internal/memory/trustset/runner.go` — `expected.extracted_memories[].status` 支持 `auto-approved` 语义
- `evals/memory/trust-set-v1.json` — 加 5 个 `auto-approved` 场景
- `internal/memory/extractor/live_provider_test.go` — 加 1 个 auto-Approved live 场景
- `README.md` — 勾选 M3 Slice 03 + 新增 M3 Slice 04 占位
- `docs/superpowers/specs/2026-08-24-m3-slice-02-extractor-design.md` — 加注 auto-Approve 决策在 app 层（Hybrid 仍纯函数）

### Interfaces Introduced

```go
// internal/session/event_row.go (modified)
// 字段不变；EventRow struct 不动（SourceRef string 仍存在）
// 投影逻辑由 internal/session/sqlite_store.go 的 Events() method 内部更新

// internal/events/event.go (modified)
type ToolCompleted struct {
    // ... 既有字段 ...
    SourceCommand string `json:"source_command,omitempty"`  // NEW
}

// internal/memory/extractor/whitelist.go (new)
type FingerprintPattern func(claim string) bool
var fingerprintPatterns = []FingerprintPattern{...}  // 8 项
func ShouldAutoApprove(claim string) bool

// internal/agent/runtime.go (modified)
type RunResult struct {
    // ... 既有字段 ...
    AutoApprovedCount int `json:"auto_approved_count,omitempty"`  // NEW
}

// internal/memory/trustset/runner.go (modified)
// Expected.ExtractedMemories[].Status 字符串支持 "auto-approved"（语义 = "active"）
```

---

### Task 1: Schema 修复 — `009_memory_source_command.sql` + Events projection

**Files:**
- Create: `internal/session/migrations/009_memory_source_command.sql`
- Modify: `internal/session/sqlite_store.go` (Events method projection)
- Modify: `internal/events/event.go` (ToolCompleted struct add SourceCommand)
- Test: `internal/session/sqlite_store_test.go` (or new `event_row_test.go`)

**Interfaces:**
- Consumes: 既有 `events` 表 schema (`001_session_event_store.sql`)
- Produces: `EventRow.SourceRef` 优先 `events.source_command`，fallback `events.ToolCompleted.Summary`；`events.ToolCompleted.SourceCommand` 字段

- [ ] **Step 1: 写失败测试**

Create new `internal/session/event_row_test.go`:

```go
package session

import (
    "context"
    "path/filepath"
    "testing"
    "time"

    "github.com/Scorpio69t/mengdie-code/internal/events"
    "github.com/Scorpio69t/mengdie-code/internal/session"
)

func TestEventsProjectionPrefersSourceCommand(t *testing.T) {
    projectRoot := t.TempDir()
    s, _ := session.OpenSQLite(context.Background(), session.OpenOptions{
        DataDir: t.TempDir() + "/data", ProjectRoot: projectRoot, Now: time.Now,
    })
    defer s.Close()
    // 触发 1 个 shell 成功事件，payload 含 source_command
    s.AppendEvent("sess1", "run1", events.KindToolCompleted, events.ToolCompleted{
        Tool: "shell", Success: true,
        Summary: "完成",  // fallback
        SourceCommand: "go test ./...",
    })
    rows, _ := s.Events(context.Background(), "sess1", 100)
    if len(rows) != 1 { t.Fatal("expected 1 row") }
    if rows[0].SourceRef != "go test ./..." { t.Fatalf("want source_command, got %q", rows[0].SourceRef) }
}

func TestEventsProjectionFallbackSummary(t *testing.T) {
    // 不写 SourceCommand → fallback 到 Summary
    s.AppendEvent("sess1", "run1", events.KindToolCompleted, events.ToolCompleted{
        Tool: "shell", Success: true, Summary: "完成",  // SourceCommand 缺省
    })
    rows, _ := s.Events(context.Background(), "sess1", 100)
    if rows[0].SourceRef != "完成" { t.Fatalf("want Summary fallback, got %q", rows[0].SourceRef) }
}
```

> Note: `s.AppendEvent` may not exist as-is; check `internal/session/sqlite_store.go` for the actual emit method (might be `AppendEvent` / `WriteEvent` / via `*EventSink`). Adapt the test to the real API. If no emit method exists, the test exercises via the `events` package's `AppendEvent` test path; if even that's missing, use the lowest-level appender exposed.

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/session -run "TestEventsProjection" -count=1 -v`
Expected: FAIL（`SourceCommand` 字段不存在 or `Events` 投影不用它）

- [ ] **Step 3: 写 migration**

Create `internal/session/migrations/009_memory_source_command.sql`:

```sql
-- 009_memory_source_command.sql
-- 给 events 表加 source_command 列，让 ruleGoTest / ruleGoLint 在生产能触发。
-- 来源：Agent shell 工具在 ToolCompleted.Summary 之外把 ShellArgs 拼到 source_command JSON 字段。
ALTER TABLE events ADD COLUMN source_command TEXT;
```

- [ ] **Step 4: 给 `events.ToolCompleted` 加 `SourceCommand` 字段**

Modify `internal/events/event.go` 的 `ToolCompleted` struct:

```go
type ToolCompleted struct {
    CallID        string `json:"call_id,omitempty"`
    Tool          string `json:"tool,omitempty"`
    Success      bool   `json:"success"`
    Summary      string `json:"summary,omitempty"`
    SourceCommand string `json:"source_command,omitempty"`  // NEW
    DurationMS   int64  `json:"duration_ms,omitempty"`
}
```

- [ ] **Step 5: 改 `Events` method 投影**

In `internal/session/sqlite_store.go`, find the `Events` method (around line 860-940) and modify the `rows.Scan(...)` + row-build to include `SourceCommand` and prefer it over Summary:

```go
// In the rows.Scan call, add &sourceCommand
var sourceCommand sql.NullString
if err := rows.Scan(..., &sourceCommand); err != nil { ... }

// In the row assembly (around line 920-930), after Scan:
if sourceCommand.Valid && sourceCommand.String != "" {
    row.SourceRef = sourceCommand.String
} else {
    row.SourceRef = p.Summary  // existing line
}
```

Read the exact `Events` method to apply this surgically.

- [ ] **Step 6: 跑测试确认 pass**

Run: `go test ./internal/session -run "TestEventsProjection" -count=1 -v`
Expected: PASS

- [ ] **Step 7: 跑全 session 测试确认无回归**

Run: `go test -race ./internal/session -count=1`
Expected: 全 PASS（含 30 旧 slice 02 测试 + 新 2 测试）

- [ ] **Step 8: 跑 migration 集成测试**

If `internal/session/sqlite_store_test.go` has a migration test that asserts the events table columns, update it to expect `source_command` is in the schema. Run:

```bash
go test -race ./internal/session -run "TestMigration" -count=1 -v
```

Expected: PASS after the column is added. If the test currently asserts the table doesn't have `source_command` (unlikely), update the assertion.

- [ ] **Step 9: Commit**

```bash
git add internal/session/migrations/009_memory_source_command.sql internal/events/event.go internal/session/sqlite_store.go internal/session/event_row_test.go
git commit -m "feat(sessions): add source_command column + EventRow projection"
```

---

### Task 2: Agent shell-tool 写 `SourceCommand` 字段

**Files:**
- Modify: `internal/agent/runtime.go` (shell-tool emit path around line 864)

**Interfaces:**
- Consumes: `events.ToolCompleted` 现在有 `SourceCommand` 字段（Task 1 改的）
- Produces: shell 工具 emit 的 `ToolCompleted.SourceCommand` 字段填 `ShellArgs` 拼成的字符串

- [ ] **Step 1: 写失败测试**

在 `internal/agent/runtime_extractor_test.go` 或新文件 `internal/agent/runtime_shell_test.go`：

```go
func TestShellToolEmitsSourceCommandInToolCompleted(t *testing.T) {
    // 起一个 stub Provider，触一次 shell 工具 (e.g., "go test ./...")
    // 断言捕获的 ToolCompleted 事件 payload 中 SourceCommand == "go test ./..."
    // （用现有的 stubProvider + ToolCaptureSink helper）
}
```

读 `internal/agent/runtime_test.go` 看 `scriptedProvider` / 现有 capture pattern，照写。如果 `runtime_test.go` 的 capture 不能区分 `Summary` 与 `SourceCommand`，在那个文件新增 1 case。

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/agent -run "TestShellToolEmitsSourceCommand" -count=1 -v`
Expected: FAIL（`SourceCommand` 字段为空或等于 `Summary`）

- [ ] **Step 3: 改 `runtime.go` shell-tool emit**

在 `internal/agent/runtime.go` line 864 附近（成功路径的 `events.KindToolCompleted` emit），把 `Summary: "完成"` 的 emit 改成同时填 `SourceCommand`：

```go
// 找到 emit:
if _, err := emitter.Emit(ctx, events.KindToolCompleted, events.ToolCompleted{
    CallID: call.ID, Tool: call.Name, Success: true, Summary: "完成", DurationMS: duration.Milliseconds(),
}); err != nil {
    return toolOutcome{fatal: err}
}

// 改成（读 prepared.ShellArgs 之前先确认 call.Arguments 是否含 ShellArgs）：
sourceCommand := joinShellArgs(call, prepared)  // helper 见下
if _, err := emitter.Emit(ctx, events.KindToolCompleted, events.ToolCompleted{
    CallID: call.ID, Tool: call.Name, Success: true,
    Summary: "完成",
    SourceCommand: sourceCommand,
    DurationMS: duration.Milliseconds(),
}); err != nil {
    return toolOutcome{fatal: err}
}

// helper（在 runtime.go 同包）：
func joinShellArgs(call provider.ToolCall, prepared tools.PreparedShellArgs) string {
    // 安全 fallback：如果 prepared.ShellArgs 为空，至少返回 Tool 名
    if len(prepared.ShellArgs) == 0 {
        return call.Name
    }
    return strings.Join(prepared.ShellArgs, " ")
}
```

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/agent -run "TestShellToolEmitsSourceCommand" -count=1 -v`
Expected: PASS

- [ ] **Step 5: 跑全 agent + extractor 测试确认无回归**

Run: `go test -race ./internal/agent ./internal/memory/extractor -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/agent/runtime.go internal/agent/*_test.go
git commit -m "feat(agent): write shell source_command in ToolCompleted events"
```

---

### Task 3: fingerprint auto-Approve — `internal/memory/extractor/whitelist.go`

**Files:**
- Create: `internal/memory/extractor/whitelist.go`
- Create: `internal/memory/extractor/whitelist_test.go`
- Test: `TestEachFingerprintPattern` + `TestShouldAutoApprove` + `TestNonFingerprintDoesNotMatch`

**Interfaces:**
- Consumes: 6 个 rules claim 文本（已知）+ 2 个 fingerprint claim（"中文 README"、"stderr"）
- Produces: `ShouldAutoApprove(claim string) bool` 顶层函数 + 8 个 pattern 函数

- [ ] **Step 1: 写失败测试**

Create `internal/memory/extractor/whitelist_test.go`:

```go
package extractor

import (
    "strings"
    "testing"
)

func TestShouldAutoApproveEachPattern(t *testing.T) {
    cases := []struct {
        claim string
        want  bool
        name  string
    }{
        {"项目使用 edit_file 修改文件", true, "edit_file"},
        {"项目使用 write_file 创建或覆盖文件", true, "write_file"},
        {"项目测试入口是 go test ./...", true, "go_test"},
        {"项目使用 golangci-lint 做静态检查", true, "golangci_lint"},
        {"本次 Agent Run 整体成功", true, "run_success"},
        {"Provider 协议层不稳定", true, "provider_unstable"},
        {"项目偏好中文 README", true, "chinese_readme"},
        {"项目偏好 stderr 优先", true, "stderr_first"},
        {"用户偏好每次都跑 npm test", false, "non_fingerprint"},
        {"项目代码 review 时使用中文", false, "non_fingerprint_chinese"},
        {"", false, "empty"},
    }
    for _, c := range cases {
        if got := ShouldAutoApprove(c.claim); got != c.want {
            t.Errorf("%s: ShouldAutoApprove(%q) = %v, want %v", c.name, c.claim, got, c.want)
        }
    }
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory/extractor -run "TestShouldAutoApprove" -count=1 -v`
Expected: FAIL（`ShouldAutoApprove` undefined）

- [ ] **Step 3: 写实现**

Create `internal/memory/extractor/whitelist.go`:

```go
// Package extractor — fingerprint whitelist for v0.1 auto-Approve.
//
// fingerprintPatterns is the package-level list of patterns the hybrid
// extraction pipeline uses to decide whether a candidate should be
// auto-approved (ProposeMemory → Approve in one shot) or stay proposed
// (awaiting user review). The list is static for v0.1; v0.2 may load from
// config or learn from accept frequency.
package extractor

import "strings"

// FingerprintPattern returns true if the given claim matches the pattern.
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

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/memory/extractor -run "TestShouldAutoApprove" -count=1 -v`
Expected: 10 cases 全 PASS

- [ ] **Step 5: 跑全 extractor 测试确认无回归**

Run: `go test -race ./internal/memory/extractor -count=1`
Expected: 全 PASS（含 21 旧 + 10 新 = 31 测试）

- [ ] **Step 6: Commit**

```bash
git add internal/memory/extractor/whitelist.go internal/memory/extractor/whitelist_test.go
git commit -m "feat(extractor): add fingerprint whitelist for v0.1 auto-Approve"
```

---

### Task 4: Agent 钩子分两阶段 + RunResult.AutoApprovedCount

**Files:**
- Modify: `internal/agent/runtime.go` (`applyMemoryExtraction` + `RunResult` struct)
- Test: `internal/agent/runtime_extractor_test.go` (or new file)

**Interfaces:**
- Consumes: `extractor.ShouldAutoApprove` (Task 3); `memory.Store.ProposeMemory` + `memory.Store.Approve` (existing)
- Produces: `RunResult.AutoApprovedCount int` 公开 JSON tag `auto_approved_count`

- [ ] **Step 1: 写失败测试**

Add to `internal/agent/runtime_extractor_test.go` (or new file):

```go
func TestRunAppliesExtractionTwoPhaseWithAutoApprove(t *testing.T) {
    // stub extractor returns 2 candidates: 1 fingerprint (e.g., "项目使用 edit_file 修改文件"), 1 non-fingerprint
    // stub store: track ProposeMemory calls and Approve calls
    // assert: ProposeMemory called 2 times, Approve called 1 time (only for fingerprint), RunResult.AutoApprovedCount == 1
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/agent -run "TestRunAppliesExtractionTwoPhaseWithAutoApprove" -count=1 -v`
Expected: FAIL（`AutoApprovedCount` 不存在 or 调用 Approve 的 stub 没收到调用）

- [ ] **Step 3: 改 `RunResult` struct**

In `internal/agent/runtime.go` `RunResult` struct (around line 105), add field:

```go
type RunResult struct {
    // ... 既有字段 ...
    AutoApprovedCount int `json:"auto_approved_count,omitempty"`
}
```

- [ ] **Step 4: 改 `applyMemoryExtraction` 两阶段**

In `internal/agent/runtime.go` `applyMemoryExtraction` method (around line 560-591), replace the body with:

```go
func (a *Agent) applyMemoryExtraction(ctx context.Context, request RunRequest) {
    if a.memoryExtractor == nil || a.memoryStore == nil || a.projectIdentity == "" {
        return
    }
    extCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
    defer cancel()
    candidates, err := a.memoryExtractor.Extract(extCtx, request.RunID)
    if err != nil || len(candidates) == 0 { return }
    if len(candidates) > 5 { candidates = candidates[:5] }

    var autoApprovedCount int
    for _, mem := range candidates {
        if mem.Scope.Value == "" {
            mem.Scope = memory.Scope{Kind: "project", Value: a.projectIdentity}
        }
        if mem.Source.Ref == "" {
            mem.Source = memory.SourceRef{
                Type: memory.SourceTypeAgentMessage,
                Ref:  request.RunID + ":extractor",
            }
        }
        stored, err := a.memoryStore.ProposeMemory(extCtx, mem)
        if err != nil { a.warnExtraction(ctx, "memory_extractor_propose_failed", err); continue }

        if extractor.ShouldAutoApprove(stored.Claim) {
            if err := a.memoryStore.Approve(extCtx, stored.ID); err != nil {
                a.warnExtraction(ctx, "auto_approve_approve_failed", err)
                continue
            }
            autoApprovedCount++
        }
    }
    a.lastAutoApprovedCount = autoApprovedCount  // 写入 agent 字段
}
```

- [ ] **Step 5: 改 `Run` 把计数写回 `RunResult`**

In `internal/agent/runtime.go` `Agent.Run` 的 final-return 路径（成功路径和 `state.result("")` 路径都需），在 `return state.result(summary), nil` 前填：

```go
a.applyMemoryExtraction(ctx, request)
result := state.result(summary)
result.AutoApprovedCount = a.lastAutoApprovedCount
return result, nil
```

同样路径给 `state.result("")`（无 tool call 路径）也加。

- [ ] **Step 6: 加 `Agent.lastAutoApprovedCount` 字段**

In `internal/agent/runtime.go` `Agent` struct，加字段：

```go
type Agent struct {
    // ... 既有字段 ...
    lastAutoApprovedCount int
}
```

- [ ] **Step 7: 跑测试确认 pass**

Run: `go test ./internal/agent -run "TestRunAppliesExtractionTwoPhaseWithAutoApprove" -count=1 -v`
Expected: PASS

- [ ] **Step 8: 跑全 agent 测试确认无回归**

Run: `go test -race ./internal/agent -count=1`
Expected: 全 PASS（含 2 旧 + 新 1 = 3 测试）

- [ ] **Step 9: Commit**

```bash
git add internal/agent/runtime.go internal/agent/*_test.go
git commit -m "feat(agent): split extraction hook into propose + auto-approve phases"
```

---

### Task 5: CLI `--status auto-approved` 翻译 + 5 Trust Set 增量场景

**Files:**
- Modify: `internal/app/memory.go` (`list` 子命令)
- Modify: `internal/memory/trustset/runner.go` (`expected.extracted_memories[].status` 支持 `auto-approved` 语义)
- Modify: `evals/memory/trust-set-v1.json` (5 新场景)
- Modify: `internal/memory/extractor/live_provider_test.go` (1 新 live 场景)
- Modify: `README.md` (勾选 M3 Slice 03)

**Interfaces:**
- Consumes: 既有 `list` 子命令与 `--status` enum
- Produces: `--status auto-approved` 翻译为 `status=active` query

- [ ] **Step 1: 写失败测试**

在 `internal/app/memory_test.go` 加：

```go
func TestMemoryListStatusAutoApproved(t *testing.T) {
    // seed 1 行 status=active + evidence row 标 source=auto_approve
    // run `list --status auto-approved` 应返回 1 行
    // 现有 list 测试中找最接近的 seed pattern
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/app -run "TestMemoryListStatusAutoApproved" -count=1 -v`
Expected: FAIL（`--status auto-approved` 不在 enum 或 query 不识别）

- [ ] **Step 3: 改 `list` 子命令**

In `internal/app/memory.go`，找到 `--status` 字符串集（v0.1 是 `proposed|active|stale|disputed|superseded|archived`），加 `auto-approved`，query 翻译：

```go
case "auto-approved":
    q.Status = "active"
    // 进一步：v0.1 简化为 status=active（不查 evidence.source）
    // v0.2 加 exact evidence.source=auto_approve 过滤
```

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/app -run "TestMemoryListStatusAutoApproved" -count=1 -v`
Expected: PASS

- [ ] **Step 5: 改 `runner.go` 支持 `auto-approved` 语义**

In `internal/memory/trustset/runner.go` 的 `expectedMatches`（around line 813），加 case：

```go
case "auto-approved":
    if got.Authority != memory.AuthorityRepository && got.Authority != memory.AuthorityVerified {
        return false
    }
    return got.Status == memory.StatusActive
```

- [ ] **Step 6: 加 5 Trust Set 增量场景**

Append to `evals/memory/trust-set-v1.json` `tasks` array:

```json
{"id": "auto-approved-rules-edits", "category": "inferred", "description": "rules 抽 edit_file 候选，fingerprint 命中 auto-approved", "setup": {"seed_memories": []}, "actions": [{"type": "run_run", "max_turns": 1}, {"type": "extract", "scope": "project/mengdie", "expect_proposed_count_gte": 0}], "expected": {"extracted_memories": [{"claim_contains": "edit_file", "authority": "repository", "status": "auto-approved"}]}}
```

类似 4 个：
- `auto-approved-rules-tests` (authority=verified, claim_contains="go test")
- `auto-approved-rules-lint` (authority=verified, claim_contains="golangci-lint")
- `auto-approved-llm-fingerprint` (authority=inferred, claim_contains="中文 README", status=auto-approved)
- `auto-approved-llm-non-fingerprint` (authority=inferred, claim_contains="npm test", status=proposed — **NOT** auto-approved)

> 验证 JSON 合法：`python -c "import json; json.load(open('evals/memory/trust-set-v1.json'))"` or `go run ./cmd/mengdie-eval` 不存在的检查。直接 `python3 -c` 即可。

- [ ] **Step 7: 加 1 live-provider auto-Approved 场景**

In `internal/memory/extractor/live_provider_test.go`，加 1 个 case：合成含 `go test ./...` 的 `ToolCompleted` 事件（通过 StubProvider + `SourceCommand: "go test ./..."` 字段），call `Hybrid.Extract` + `splitForAutoApprove`，验证返回 1 个 auto-approved candidate。

- [ ] **Step 8: 跑全测试 + 验证 Trust Set 40 场景**

Run:
```bash
go test -race ./internal/memory/... -count=1
go test -race ./internal/app -count=1
python3 -c "import json; d=json.load(open('evals/memory/trust-set-v1.json')); print(f'tasks: {len(d[\"tasks\"])}'); cats={}; [cats.__setitem__(t['category'], cats.get(t['category'],0)+1) for t in d['tasks']]; print(cats)"
```

Expected: tasks: 40, distribution: explicit 15, repository 5, verified 5, inferred 10, auto-approved 5 (注意 'auto-approved' scenarios 用 category='inferred' 标签 + status='auto-approved' expectation)

- [ ] **Step 9: 改 `README.md`**

In `README.md` 第 107-110 行附近（M3 Slice 02 行附近），改：

```markdown
- [x] 第三阶段 Slice 03：ruleGoTest/ruleGoLint schema 修复 + fingerprint auto-Approve（[设计稿](./docs/superpowers/specs/2026-08-24-m3-slice-03-auto-approve-design.md)）
- [ ] M3 Slice 04：跨 Authority dispute 标记（待办）
- [ ] M4：默认只生成提案的复盘机制
```

- [ ] **Step 10: Commit**

```bash
git add internal/app/memory.go internal/memory/trustset/runner.go evals/memory/trust-set-v1.json internal/memory/extractor/live_provider_test.go README.md internal/app/memory_test.go
git commit -m "feat(cli): add --status auto-approved + 5 Trust Set auto-approve scenarios"
```

---

### Task 6: Live provider + CI + 文档

**Files:**
- Create: `docs/development/phase-3-slice-03/IMPLEMENTATION_REPORT.md`
- Modify: `.github/workflows/ci.yml` (add extractor step)
- Modify: `docs/superpowers/specs/2026-08-24-m3-slice-02-extractor-design.md` (add note that auto-Approve lives in app layer, Hybrid stays pure)

**Interfaces:**
- Consumes: 既有 35 Trust Set 场景 + 5 新 auto-approved
- Produces: live provider 路径 + CI 加 extractor 步骤 + spec 注 + 实施报告

- [ ] **Step 1: 写实施报告**

Create `docs/development/phase-3-slice-03/IMPLEMENTATION_REPORT.md`，结构镜像 slice 02 报告：
- 交付范围（migration + EventRow + agent + extractor + CLI + 5 场景 + live + CI + 文档）
- 关键设计 + 守则（fingerprint 包级静态、Hybrid 仍纯函数、auto-Approve 决策在 app 层、ProposeMemory 不绕过 Approve）
- 40 场景 baseline 6 指标（precision@5 / false_recall_rate / source_traceability / authority_fidelity / why_completeness / **auto_approved_rate**）
- 验证
- Follow-ups
- 红线检查

- [ ] **Step 2: 改 ci.yml**

In `.github/workflows/ci.yml` `quality` job，在 `运行 Memory Trust Set` 步骤后加：

```yaml
- name: 运行 Memory Extractor
  run: go test -race ./internal/memory/extractor/... -count=1
```

- [ ] **Step 3: 改 slice 02 spec 注**

In `docs/superpowers/specs/2026-08-24-m3-slice-02-extractor-design.md` §10 (app.Runtime 拼装) 末尾，加：

```markdown
> v0.1.1 注：M3 Slice 03 引入 fingerprint auto-Approve。Hybrid.Extract 仍返回纯 `[]Memory`，
> 决策在 app 层 `applyMemoryExtraction` 钩子分两阶段：先 ProposeMemory 所有候选，
> 再对 fingerprint 命中的 id 调 Store.Approve。RunResult.AutoApprovedCount 公开
> 计数。详见 `docs/superpowers/specs/2026-08-24-m3-slice-03-auto-approve-design.md`。
```

- [ ] **Step 4: 跑最终质量门禁**

```bash
gofmt -l .                       # 0
go vet ./...                    # clean
go test -race ./...             # 除 Windows shell pre-existing 外全 PASS
golangci-lint run ./...         # 0 issue
govulncheck@v1.1.4 ./...        # No vulns
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64  go build ./cmd/...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64  go build ./cmd/...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64   go build ./cmd/...
go test -tags=liveprovider -run TestLiveProviderAutoApproveMemory ./internal/memory/extractor/  # SKIP 无 env
```

Expected: 除 pre-existing Windows `TestShellExecute` 外全 PASS

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml docs/development/phase-3-slice-03/IMPLEMENTATION_REPORT.md docs/superpowers/specs/2026-08-24-m3-slice-02-extractor-design.md
git commit -m "ci+docs: add extractor step + impl report + slice-02 spec note"
```

---

## Execution Order

| Order | Task | Output |
|---|---|---|
| 1 | Task 1: Schema 修复 — 009 + Events 投影 | `internal/session/migrations/009_memory_source_command.sql` + `events.ToolCompleted.SourceCommand` + EventRow 投影 |
| 2 | Task 2: Agent shell-tool 写 SourceCommand | shell emit 改造 + 测试 |
| 3 | Task 3: fingerprint auto-Approve — whitelist.go | 8 patterns + `ShouldAutoApprove` |
| 4 | Task 4: Agent 钩子两阶段 + RunResult | `applyMemoryExtraction` 分两阶段 + `AutoApprovedCount` |
| 5 | Task 5: CLI + 5 Trust Set 场景 + 1 live | 6 指标 baseline 输出 |
| 6 | Task 6: Live provider + CI + 文档 | impl report + ci.yml + spec 注 |

## Final Gates

```bash
gofmt -l .                                  # must be empty
go vet ./...                               # clean
go test -race ./...                        # 除 Windows pre-existing TestShellExecute 外全 PASS
go test -race ./internal/memory/... -run TestMemoryTrustSetV1  # 40 场景通过
go test -race ./internal/memory/extractor/...  # 31+ 测试通过
go test -tags=liveprovider -run TestLiveProviderAutoApproveMemory ./internal/memory/extractor/  # SKIP 无 env；有 env 时跑真 Provider
golangci-lint run ./...                    # 0 issue
govulncheck@v1.1.4 ./...                   # No vulns
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64  go build ./cmd/...  # OK
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64  go build ./cmd/...  # OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/...  # OK
CGO_ENABLED=0 GOOS=linux GOARCH=amd64   go build ./cmd/...  # OK
```

## Beads Close

CI 全过、user 审核 merge 后：

```bash
bd close mengdie-9xd --reason="M3 Slice 03 完成：6 task 全部 ship，40 Trust Set 场景 baseline 6 指标全在 [0,1]，live provider auto-Approved 场景 SKIP 保护就绪，4 目标构建 + golangci-lint + govulncheck 全过，PR ready-for-review"
```
