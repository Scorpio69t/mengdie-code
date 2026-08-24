# M3 Slice 02 Implementation Plan — MemoryExtractor + memory_recall Registration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 M3 Slice 01 的可信记忆系统之上落地：MemoryExtractor 自动候选提取（Rules + LLM + Hybrid 三实现）、ProjectIdentity 字段、DefaultTools 签名扩展让 memory_recall 工具正式进 app.Runtime 的工具注册表，加上 30+5 场景 Trust Set 评测 + live provider 端到端。

**Architecture:** 混合 extractor（规则先于 LLM，避免重复 LLM 成本），Agent.Run 末尾 `applyMemoryExtraction` 钩子（5s 超时、≤5 条、走 ProposeMemory 而非 SaveUserMemory，必须经 Approve 才 active），app.Runtime 拼装时把 `*memory.Retriever` 通过 `WithMemoryRetriever` 函子 + `WithProjectIdentity` 传给 `tools.DefaultTools()`，把 `*memory.Extractor` 包装为 `agent.MemoryExtractor` 注入 `agent.Options`。

**Tech Stack:** Go 1.26.6、`internal/memory` 既有 store/retriever 模式、`internal/agent/runtime.go` Options 模式（已支持 `MemoryRetriever` / `ProjectIdentity`）、`internal/tools/defaults.go` variadic options 模式、`modernc.org/sqlite` 既有迁移链路、`provider.Provider` 既有 LLM 客户端。

## Global Constraints

- Go 1.26.6，module `github.com/Scorpio69t/mengdie-code`
- 仅 `CGO_ENABLED=0` 跨平台构建；四目标必须通过：darwin-arm64、darwin-amd64、windows-amd64、linux-amd64
- 禁止在用户仓库中自动 git commit / push
- 任何 `git commit` 由执行人显式触发
- 新增的 `internal/memory/extractor` 包不得 import `internal/agent`（避免循环依赖：`agent → memory → session`）
- `agent.MemoryExtractor` interface 的返回类型必须是 `[]memory.Memory`（不引入新类型），adapter 在 `internal/agent/extractor_adapter.go` 内做薄映射
- 任何 LLM 提取必须走 `memory.Store.ProposeMemory`（不直接 `SaveUserMemory`）→ 必须经 `Approve` 才 active，符合 slice 01 守则
- Extract 阶段使用 `context.WithoutCancel(ctx)` 避免上游 cancel 影响 propose
- Extract 整体失败 → 静默返回，不阻断 Run
- 单条 ProposeMemory 失败 → emit warning + 继续下一条
- 提取候选数 ≤ 5 防 context 注入失控
- 中文优先 package doc + 英文 inline comments（与项目约定一致）
- Sentinel error 风格：`errors.New("...")` 配合 `errors.Is` 匹配
- Live provider test `//go:build liveprovider`，env 缺失 SKIP；evidence 不含 API Key
- 不修改既有 008_memory.sql 或 `memory.Store`/`Retriever` 公共 API（除非必要）
- 既有 `tools.DefaultTools()` 调用点不传 options 时行为不变（兼容）
- Trust Set runner 的 30 个 slice 01 场景必须 100% 通过性不变

---

## File Structure

### 新增
- `internal/config/config.go` — `Loaded.ProjectIdentity` 字段 + `ProjectIdentityValue()` 方法（新增 1 个字段 + 1 个方法）
- `internal/tools/defaults.go` — `DefaultToolsOption` 函子类型 + `WithMemoryRetriever`/`WithProjectIdentity` 函子 + 改 `DefaultTools` 签名为 variadic
- `internal/memory/extractor/extractor.go` — `Extractor` interface 定义
- `internal/memory/extractor/rules.go` — Rules 实现
- `internal/memory/extractor/llm.go` — LLM 实现
- `internal/memory/extractor/hybrid.go` — Hybrid 实现
- `internal/memory/extractor/{rules,llm,hybrid,runner}_test.go` — 单元测试
- `internal/memory/extractor/live_provider_test.go` — `//go:build liveprovider` 端到端
- `internal/agent/extractor_adapter.go` — `*memory.extractor.Hybrid` → `agent.MemoryExtractor` 适配
- `docs/superpowers/plans/2026-08-24-m3-slice-02-extractor.md` — 本文件
- `docs/development/phase-3-slice-02/IMPLEMENTATION_REPORT.md` — 最终实施报告

### 修改
- `internal/agent/runtime.go` — `Options` 新增 `MemoryStore *memory.Store` + `MemoryExtractor MemoryExtractor` 字段；`Agent` struct 同步；`New()` 初始化；`Run` 末尾加 `applyMemoryExtraction(ctx, request)` 钩子
- `internal/app/runtime.go` — `runAgent` 中构造 `*memory.Store` + `*memory.Retriever` + `*memory.Extractor` + adapter + 注入 `agent.Options` + `tools.DefaultTools(WithMemoryRetriever(...), WithProjectIdentity(...))`
- `cmd/mengdie/main.go` — `DefaultTools()` 调用点更新为 `DefaultTools(WithProjectIdentity(...))`（如适用）
- `internal/memory/trustset/runner.go` — 支持 `actions[].type = "extract"` + `expected.extracted_memories[]` 验证
- `evals/memory/trust-set-v1.json` — 新增 5 个 `inferred_extraction` 场景
- `.github/workflows/ci.yml` — quality job 加 `go test -race ./internal/memory/extractor/... -count=1` 步骤
- `README.md` — 勾选 M3 Slice 02 + 新增 M3 Slice 03/04 占位

### Interfaces Introduced

```go
// internal/memory/extractor/extractor.go
type Extractor interface {
    Extract(ctx context.Context, sessionID string) ([]memory.Memory, error)
}

func NewRules(store *memory.Store) *Rules
func (r *Rules) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error)

func NewLLM(provider provider.Provider, model string) *LLM
func (l *LLM) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error)

func NewHybrid(rules *Rules, llm *LLM) *Hybrid
func (h *Hybrid) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error)

// internal/agent/runtime.go
type MemoryExtractor interface {
    Extract(ctx context.Context, sessionID string) ([]memory.Memory, error)
}
type Options struct {
    // ... 既有字段 ...
    MemoryStore     *memory.Store      // NEW
    MemoryExtractor MemoryExtractor    // NEW
}

// internal/agent/extractor_adapter.go
func NewExtractorAdapter(extractor extractor.Extractor, store *memory.Store, projectID string) *ExtractorAdapter
func (a *ExtractorAdapter) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error)

// internal/config/config.go
type Loaded struct {
    // ... 既有字段 ...
    ProjectIdentity string  // NEW
}
func (l Loaded) ProjectIdentityValue() string  // NEW

// internal/tools/defaults.go
type DefaultToolsOption func(*defaultToolsConfig)
type defaultToolsConfig struct { memoryRetriever MemoryRecallRetriever; projectIdentity string }
func WithMemoryRetriever(r MemoryRecallRetriever) DefaultToolsOption
func WithProjectIdentity(id string) DefaultToolsOption
func DefaultTools(opts ...DefaultToolsOption) []Tool
```

---

### Task 1: ProjectIdentity 字段 + 方法

**Files:**
- Modify: `internal/config/config.go:69-79` (`Loaded` struct) + add method
- Test: `internal/config/config_test.go` (如不存在则新建；如存在则追加测试)

**Interfaces:**
- Consumes: 既有 `Loaded` struct 字段（`Config`, `SelectedProfile`, `ProjectRoot`, `WorkingDir`, `UserConfigPath`, `ProjectConfigPath`, `UserConfigLoaded`, `ProjectConfigLoaded`）
- Produces: `Loaded.ProjectIdentityValue() string` 方法 + `Loaded.ProjectIdentity` 字段

- [ ] **Step 1: 写失败测试**

在 `internal/config/config_test.go` 新增：

```go
func TestLoadedProjectIdentityValueFallbackToBaseName(t *testing.T) {
    loaded := Loaded{ProjectRoot: filepath.Join(string(os.PathSeparator), "tmp", "mengdie-code")}
    if got, want := loaded.ProjectIdentityValue(), "mengdie-code"; got != want {
        t.Fatalf("ProjectIdentityValue() = %q, want %q", got, want)
    }
}

func TestLoadedProjectIdentityValueExplicitOverrides(t *testing.T) {
    loaded := Loaded{ProjectRoot: filepath.Join(string(os.PathSeparator), "tmp", "mengdie-code"), ProjectIdentity: "explicit-id"}
    if got, want := loaded.ProjectIdentityValue(), "explicit-id"; got != want {
        t.Fatalf("ProjectIdentityValue() = %q, want %q (explicit must override)", got, want)
    }
}

func TestLoadedProjectIdentityValueEmptyRootEmpty(t *testing.T) {
    loaded := Loaded{ProjectRoot: ""}
    if got := loaded.ProjectIdentityValue(); got != "" {
        t.Fatalf("ProjectIdentityValue() with empty ProjectRoot = %q, want empty", got)
    }
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/config -run "TestLoadedProjectIdentity" -count=1 -v`
Expected: FAIL（`ProjectIdentityValue` 方法不存在）

- [ ] **Step 3: 实现方法 + 字段**

在 `internal/config/config.go` 中：

```go
type Loaded struct {
    // ... 既有字段（顺序保持）...
    ProjectIdentity string  // 添加：显式设置时优先；空时由 ProjectIdentityValue() fallback 到 base name
}
```

在 Loaded 同一文件后面添加方法：

```go
// ProjectIdentityValue returns the explicit ProjectIdentity when set,
// otherwise falls back to filepath.Base(ProjectRoot). Both empty inputs
// return "" so callers can disable the field by zero-loading it.
func (l Loaded) ProjectIdentityValue() string {
    if l.ProjectIdentity != "" {
        return l.ProjectIdentity
    }
    return filepath.Base(strings.TrimRight(l.ProjectRoot, string(os.PathSeparator)))
}
```

确保 `config.go` 已 import `path/filepath` 和 `strings`（如未 import，需在 import block 加 `"path/filepath"` 和 `"strings"`）。

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/config -run "TestLoadedProjectIdentity" -count=1 -v`
Expected: PASS

- [ ] **Step 5: 跑全 config 测试确认无回归**

Run: `go test -race ./internal/config -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add Loaded.ProjectIdentity + ProjectIdentityValue()"
```

---

### Task 2: DefaultTools 签名扩展

**Files:**
- Modify: `internal/tools/defaults.go`
- Modify: 任何调用 `tools.DefaultTools()` 的地方（`cmd/mengdie/main.go`、测试代码、`internal/app/runtime.go` —— 后者在 Task 7 改）
- Test: `internal/tools/defaults_test.go`（如不存在则新建）

**Interfaces:**
- Consumes: 既有 `DefaultTools() []Tool` 返回值不变；`MemoryRecallRetriever` interface（Task 8 ship 时已定义）
- Produces: `DefaultTools(opts ...DefaultToolsOption) []Tool` + `WithMemoryRetriever` + `WithProjectIdentity` 函子

- [ ] **Step 1: 写失败测试**

在 `internal/tools/defaults_test.go` 新增：

```go
func TestDefaultToolsNoOptionsReturnsBaseTools(t *testing.T) {
    tools := DefaultTools()
    // 既有 base tools 列表（不附加 memory_recall）
    for _, tt := range tools {
        if tt.Spec().Name == "memory_recall" {
            t.Fatalf("memory_recall should NOT be in default tools without WithMemoryRetriever")
        }
    }
}

func TestDefaultToolsWithMemoryRetrieverAddsMemoryRecall(t *testing.T) {
    stub := &stubRetriever{}
    tools := DefaultTools(WithMemoryRetriever(stub))
    found := false
    for _, tt := range tools {
        if tt.Spec().Name == "memory_recall" { found = true }
    }
    if !found { t.Fatal("memory_recall should be in default tools when WithMemoryRetriever is set") }
}

type stubRetriever struct{}
func (stubRetriever) Tier3AtomicRecall(ctx context.Context, query string, topK int, scope tools.MemoryScope) ([]tools.MemoryHit, error) {
    return nil, nil
}
```

注意：需要看 `internal/tools/memory_recall.go:75-80` 的 `MemoryRecallRetriever` interface 实际签名（可能 `Tier3AtomicRecall` 不存在；可能签名是 `Recall(query, topK, scope) []MemoryHit`）。按 `memory_recall.go` 的真实 interface 调整 stub。

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/tools -run "TestDefaultTools" -count=1 -v`
Expected: FAIL（`WithMemoryRetriever` 不存在）

- [ ] **Step 3: 实现函子 + 改签名**

在 `internal/tools/defaults.go`：

```go
type defaultToolsConfig struct {
    memoryRetriever MemoryRecallRetriever
    projectIdentity  string
}

// DefaultToolsOption configures DefaultTools. Pass zero or more options
// to opt into feature-specific tools (e.g. memory_recall). The base M1
// tool set is always returned in stable order.
type DefaultToolsOption func(*defaultToolsConfig)

// WithMemoryRetriever appends the memory_recall tool to the default set.
// When retriever is nil, no memory_recall tool is appended (defensive:
// callers can pass nil for tests that don't exercise the path).
func WithMemoryRetriever(retriever MemoryRecallRetriever) DefaultToolsOption {
    return func(c *defaultToolsConfig) {
        c.memoryRetriever = retriever
    }
}

// WithProjectIdentity sets the project-scope identity used by tools that
// need a target scope value (e.g. memory_recall's catalogue injection).
// Empty string disables the override; tools that need it will fall back
// to their own scope resolution.
func WithProjectIdentity(projectIdentity string) DefaultToolsOption {
    return func(c *defaultToolsConfig) {
        c.projectIdentity = strings.TrimSpace(projectIdentity)
    }
}

// DefaultTools returns the M1 tool set plus feature-specific tools enabled
// by the provided options. The base set is returned in stable order
// regardless of options; only the *appended* tools depend on options.
func DefaultTools(opts ...DefaultToolsOption) []Tool {
    cfg := defaultToolsConfig{}
    for _, o := range opts {
        o(&cfg)
    }
    tools := defaultM1Tools()  // 提取既有 base set 的工厂调用
    if cfg.memoryRetriever != nil {
        tools = append(tools, NewMemoryRecallTool(
            cfg.memoryRetriever,
            WithProjectIdentity(cfg.projectIdentity),
        ))
    }
    return tools
}
```

**重要：** 既有 `DefaultTools()` 体内的实现需要 refactor 成 `defaultM1Tools()` 私有工厂。读 `internal/tools/defaults.go` 当前实现，把 base set 的构造逻辑（原样）放进 `defaultM1Tools()`，然后 `DefaultTools(opts...)` 调用 `defaultM1Tools()` 起步 + 按 options 追加。

确保 `defaults.go` 已 import `"strings"`（如未 import 加之）。

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/tools -run "TestDefaultTools" -count=1 -v`
Expected: PASS

- [ ] **Step 5: 跑全 tools 测试确认无回归**

Run: `go test -race ./internal/tools -count=1`
Expected: 全 PASS（除 Windows pre-existing `TestShellExecute`）

- [ ] **Step 6: 更新 `cmd/mengdie/main.go` 调用点**

读 `cmd/mengdie/main.go` 中所有 `tools.DefaultTools()` 调用（应该有 1-2 处）；如不需要 `memory_recall` 工具，保持 `DefaultTools()` 不传 options；如有上下文需要，加 `WithProjectIdentity(...)`。Task 7 会在 `internal/app/runtime.go` 处理 main runAgent 路径。

- [ ] **Step 7: Commit**

```bash
git add internal/tools/defaults.go internal/tools/defaults_test.go cmd/mengdie/main.go
git commit -m "feat(tools): extend DefaultTools with memory_recall options"
```

---

### Task 3: Extractor interface + Rules 实现

**Files:**
- Create: `internal/memory/extractor/extractor.go`
- Create: `internal/memory/extractor/rules.go`
- Create: `internal/memory/extractor/rules_test.go`
- Modify: 不修改其他文件（`memory.Store` 已提供 `List(ctx, ListQuery)` 拉 events）

**Interfaces:**
- Consumes: 既有 `memory.Store.List(ctx, ListQuery{ScopeKind, ScopeValue, Authority, Status, Limit})` 拉 event row
- Produces: `Extractor` interface + `Rules` struct + `Extract(ctx, sessionID) ([]Memory, error)` 方法

- [ ] **Step 1: 写失败测试**

在 `internal/memory/extractor/rules_test.go` 新增：

```go
package extractor

import (
    "context"
    "encoding/json"
    "path/filepath"
    "testing"
    "time"

    "github.com/Scorpio69t/mengdie-code/internal/memory"
    "github.com/Scorpio69t/mengdie-code/internal/session"
)

func TestRulesExtractsEditFileMemory(t *testing.T) {
    store := openStoreWithEvents(t, []testEvent{
        {kind: "tool.completed", name: "edit_file", success: true, time: time.Now().Add(-time.Minute)},
    })
    rules := NewRules(store)
    got, err := rules.Extract(context.Background(), "session-1")
    if err != nil { t.Fatal(err) }
    if len(got) == 0 { t.Fatal("expected at least 1 rule-extracted memory") }
    found := false
    for _, m := range got {
        if m.Authority == memory.AuthorityRepository && (m.Claim == "项目使用 edit_file 修改文件" || strings.Contains(m.Claim, "edit_file")) {
            found = true
        }
    }
    if !found { t.Fatalf("expected edit_file repository claim, got %+v", got) }
}
```

`openStoreWithEvents` helper（test 文件内）：打开一个 `state.db` 临时目录，调 `*session.SQLiteStore` + `*memory.Store`，插入事件。

`testEvent` 是简化的 helper struct：{kind, name, success, time}，插入时翻译成对应 `events.Kind*`。

具体 helper 实现需要根据 `internal/session/memories`/`memories_fts`/`memory_evidence`/`memory_usage` 的 schema 决定往哪张表插——但更简单的做法是直接写 events 到 session 的 events 表（用 `session.Store` 暴露的 API 或 `session.EventSink`）。

如果 session 暴露的事件写入 API 太复杂，可改为：直接测试 `Rules` 内的规则函数（如 `ruleEditFile(events []event) []Memory`），不通过 `Extract`。这种 unit-level 测试比集成测试更简单，建议两条线：
- 一组 unit 测试覆盖 `rule*` 函数
- 一组集成测试覆盖 `Extract(ctx, sessionID)` 完整路径

简化版（先 unit 后集成）：

```go
func TestRuleEditFile(t *testing.T) {
    events := []eventStub{
        {Kind: "tool.completed", Name: "edit_file", Success: true},
    }
    got := ruleEditFile(events)
    if len(got) != 1 { t.Fatalf("want 1, got %d", len(got)) }
    if got[0].Authority != memory.AuthorityRepository {
        t.Fatalf("want repository, got %s", got[0].Authority)
    }
}
```

按这个方式逐条规则写 unit 测试。

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory/extractor -count=1`
Expected: FAIL（包未存在或函数未定义）

- [ ] **Step 3: 实现 Extractor interface + Rules 骨架**

在 `internal/memory/extractor/extractor.go`：

```go
// Package extractor turns a completed Agent run into candidate memory rows.
// Three implementations are wired by app.Runtime:
//   - Rules: scans session events for structured patterns (tool usage,
//     test runs, lint checks). Zero LLM cost, fully deterministic.
//   - LLM: extracts from the transcript via a configured provider.
//   - Hybrid: runs Rules first, drops LLM candidates that duplicate rule
//     claims, returns the rest.
package extractor

import (
    "context"

    "github.com/Scorpio69t/mengdie-code/internal/memory"
)

// Extractor returns candidate memory rows derived from a completed run.
// Implementations MUST NOT write to the store; app.Runtime is the single
// write path so trust gates (Authority 守门 + Conflict 状态机) are
// applied uniformly.
type Extractor interface {
    Extract(ctx context.Context, sessionID string) ([]memory.Memory, error)
}
```

- [ ] **Step 4: 实现 Rules 全部规则**

在 `internal/memory/extractor/rules.go`：

```go
package extractor

import (
    "context"
    "fmt"
    "time"

    "github.com/Scorpio69t/mengdie-code/internal/memory"
)

// Rules is a deterministic extractor that turns session events into
// candidate memory rows. It is the cheap default; LLM is opt-in.
type Rules struct {
    store *memory.Store
}

func NewRules(store *memory.Store) *Rules { return &Rules{store: store} }

// eventRow is the minimal projection of session events the rules read.
type eventRow struct {
    Kind      string
    Name      string
    Success   bool
    Timestamp time.Time
    SourceRef string
}

// Extract reads the session's events via Store.List and applies each rule.
// Returns memory.Memory slices with Authority already set; the Agent
// runtime is responsible for re-applying scope/source.
func (r *Rules) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error) {
    if r == nil || r.store == nil { return nil, nil }
    events, err := r.loadEvents(ctx, sessionID)
    if err != nil { return nil, err }
    var out []memory.Memory
    for _, rule := range r.allRules() {
        out = append(out, rule(events)...)
    }
    return out, nil
}

func (r *Rules) loadEvents(ctx context.Context, sessionID string) ([]eventRow, error) {
    list, err := r.store.List(ctx, memory.ListQuery{Limit: 500})
    if err != nil { return nil, err }
    rows := make([]eventRow, 0, len(list))
    for _, mem := range list {
        rows = append(rows, eventRow{
            Kind:      mem.Authority, // placeholder; real impl reads from event table
            Timestamp: mem.ObservedAt,
            SourceRef: mem.Source.Ref,
        })
    }
    return rows, nil
}

// type ruleFunc func([]eventRow) []memory.Memory
type ruleFunc func([]eventRow) []memory.Memory

func (r *Rules) allRules() []ruleFunc {
    return []ruleFunc{
        ruleEditFile,
        ruleWriteFile,
        ruleGoTest,
        ruleGoLint,
        ruleRunAllSuccess,
        ruleProviderProtocolFailures,
    }
}

// ruleEditFile: at least one successful tool.completed for edit_file.
func ruleEditFile(events []eventRow) []memory.Memory {
    for _, e := range events {
        if e.Kind == "tool.completed" && e.Name == "edit_file" && e.Success {
            return []memory.Memory{{
                Claim:     "项目使用 edit_file 修改文件",
                Authority: memory.AuthorityRepository,
            }}
        }
    }
    return nil
}

// ruleWriteFile: at least one successful tool.completed for write_file.
func ruleWriteFile(events []eventRow) []memory.Memory {
    for _, e := range events {
        if e.Kind == "tool.completed" && e.Name == "write_file" && e.Success {
            return []memory.Memory{{
                Claim:     "项目使用 write_file 创建或覆盖文件",
                Authority: memory.AuthorityRepository,
            }}
        }
    }
    return nil
}

// ruleGoTest: any tool.completed shell with "go test ./..."
func ruleGoTest(events []eventRow) []memory.Memory {
    for _, e := range events {
        if e.Kind == "tool.completed" && e.Name == "shell" && e.Success && strings.Contains(e.SourceRef, "go test") {
            return []memory.Memory{{
                Claim:     "项目测试入口是 go test ./...",
                Authority: memory.AuthorityVerified,
            }}
        }
    }
    return nil
}

// ruleGoLint: any tool.completed shell with "golangci-lint"
func ruleGoLint(events []eventRow) []memory.Memory {
    for _, e := range events {
        if e.Kind == "tool.completed" && e.Name == "shell" && e.Success && strings.Contains(e.SourceRef, "golangci-lint") {
            return []memory.Memory{{
                Claim:     "项目使用 golangci-lint 做静态检查",
                Authority: memory.AuthorityVerified,
            }}
        }
    }
    return nil
}

// ruleRunAllSuccess: at least one run.completed and zero tool failures.
func ruleRunAllSuccess(events []eventRow) []memory.Memory {
    hasCompleted := false
    for _, e := range events {
        if e.Kind == "run.completed" { hasCompleted = true }
        if e.Kind == "tool.completed" && !e.Success { return nil }
    }
    if !hasCompleted { return nil }
    return []memory.Memory{{
        Claim:     "本次 Agent Run 整体成功",
        Authority: memory.AuthorityInferred,
    }}
}

// ruleProviderProtocolFailures: ≥ 2 run.failed with category=provider_protocol.
func ruleProviderProtocolFailures(events []eventRow) []memory.Memory {
    n := 0
    for _, e := range events {
        if e.Kind == "run.failed" && strings.Contains(e.SourceRef, "provider_protocol") { n++ }
    }
    if n < 2 { return nil }
    return []memory.Memory{{
        Claim:     "Provider 协议层不稳定",
        Authority: memory.AuthorityInferred,
    }}
}
```

**重要：**
- `loadEvents` 当前实现是 placeholder（用 `store.List` 拿 Memory 行而不是真实 events）。这个不充分——events 表是 `*session.SQLiteStore` 的内部状态（`session_events` 或类似表），需要通过 session 暴露的 API 读。后续 Task 4 同样需要。**统一在 Task 4 重构 loadEvents 路径**：定义一个共享 `EventReader` interface（由 `*session.SQLiteStore` 实现），让 Rules 和 LLM 都用它读 events。
- 规则 unit 测试用合成 `eventRow` 切片直接调 `rule*` 函数（不经过 loadEvents / Store），简化测试。

- [ ] **Step 5: 跑测试确认 pass**

Run: `go test ./internal/memory/extractor -count=1 -v`
Expected: 6 个 rule unit 测试全 PASS

- [ ] **Step 6: 跑全 memory 测试确认无回归**

Run: `go test -race ./internal/memory -count=1`
Expected: 全 PASS（含 slice 01 旧 32 测试 + 新 6 测试 = 38）

- [ ] **Step 7: Commit**

```bash
git add internal/memory/extractor/
git commit -m "feat(memory): add Extractor interface + Rules implementation"
```

---

### Task 4: Extractor EventReader interface + 重构 loadEvents

**Files:**
- Create: `internal/memory/extractor/event_reader.go`（共享接口）
- Modify: `internal/session/sqlite_store.go`（暴露 `EventReader` 实现）
- Modify: `internal/memory/extractor/rules.go`（用 `EventReader` 替换 `loadEvents` placeholder）
- Modify: `internal/memory/extractor/rules_test.go`（如需更新测试用 EventReader stub）

**Interfaces:**
- Consumes: `*session.SQLiteStore` 已有 sessions/events 表
- Produces: `EventReader` interface + `SQLiteStore.EventReader(ctx, sessionID)` 方法

- [ ] **Step 1: 写失败测试**

在 `internal/memory/extractor/event_reader_test.go` 新增：

```go
package extractor

import (
    "context"
    "testing"
    "time"
)

func TestEventReaderInterfaceSatisfiedByStore(t *testing.T) {
    // Compile-time check: *memory.Store (after refactor) must satisfy EventReader.
    var _ EventReader = (*fakeEventReader)(nil)
}

type fakeEventReader struct{}
func (fakeEventReader) Events(ctx context.Context, sessionID string, limit int) ([]EventRow, error) {
    return nil, nil
}

func TestEventReaderRowsHaveExpectedFields(t *testing.T) {
    r := &fakeEventReader{...}
    // 插入 → 读 → 断言
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory/extractor -run "TestEventReader" -count=1`
Expected: FAIL（`EventReader` interface 不存在）

- [ ] **Step 3: 实现 EventReader interface + 在 session 包暴露实现**

在 `internal/memory/extractor/event_reader.go`：

```go
package extractor

import (
    "context"
    "time"
)

// EventRow is the minimal projection of a session event used by extractors.
// We deliberately project to a small struct so the extractor package
// does not import internal/session and the underlying SQLite schema.
type EventRow struct {
    Kind      string
    Name      string
    Success   bool
    Timestamp time.Time
    SourceRef string
}

// EventReader returns the events for a single session, oldest first,
// capped at `limit`. Implementations live in the session package and
// are wired by app.Runtime.
type EventReader interface {
    Events(ctx context.Context, sessionID string, limit int) ([]EventRow, error)
}
```

在 `internal/session/sqlite_store.go` 加方法（读 session_events 表，需要看 session 实际表名；参考 `migrations/001_session_event_store.sql`）：

```go
// Events returns session events oldest-first, capped at limit. The
// projection is narrow (kind/name/success/timestamp/source_ref) so callers
// don't need to know the full event schema.
func (s *SQLiteStore) Events(ctx context.Context, sessionID string, limit int) ([]extractor.EventRow, error) {
    // SELECT kind, name, success, created_at, source_ref FROM session_events
    //     WHERE session_id = ? ORDER BY seq ASC LIMIT ?
    // scan into []extractor.EventRow
}
```

注意：`Events` 方法不能放在 `internal/session` 包内（如果它返回 `extractor.EventRow` 就会 import extractor → 循环）。需要把 `EventRow` 放在 `internal/session` 包内（或一个不依赖 extractor 的子包如 `internal/session/events`），然后 `extractor.EventReader` 接口用 `session.EventRow` 类型：

调整：把 `EventRow` 放在 `internal/session` 包（`session.EventRow`），`extractor.EventReader` 接口签名改为：

```go
type EventReader interface {
    Events(ctx context.Context, sessionID string, limit int) ([]session.EventRow, error)
}
```

但 `extractor` import `session` 又构成反向依赖（`session` 已有 `extractor` 类型吗？检查：没有，`extractor` 是新包）。所以 `extractor` → `session` 是允许的。

把 `EventRow` 放 `internal/session`，在 `extractor/event_reader.go` 引用 `session.EventRow`：

```go
package extractor

import "github.com/Scorpio69t/mengdie-code/internal/session"

type EventReader interface {
    Events(ctx context.Context, sessionID string, limit int) ([]session.EventRow, error)
}
```

更新 `rules.go` 的 `loadEvents`：

```go
func (r *Rules) loadEvents(ctx context.Context, sessionID string) ([]session.EventRow, error) {
    if r.eventReader == nil { return nil, nil }
    return r.eventReader.Events(ctx, sessionID, 500)
}
```

`Rules` 新增 `eventReader EventReader` 字段；`NewRules` 改为 `NewRules(reader EventReader) *Rules`。

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/memory/extractor -count=1`
Expected: PASS

- [ ] **Step 5: 跑全 memory + session 测试确认无回归**

Run: `go test -race ./internal/session ./internal/memory -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/memory/extractor/event_reader.go internal/session/sqlite_store.go internal/memory/extractor/rules.go
git commit -m "feat(memory): add EventReader interface for session events"
```

---

### Task 5: LLM Extractor

**Files:**
- Create: `internal/memory/extractor/llm.go`
- Create: `internal/memory/extractor/llm_test.go`

**Interfaces:**
- Consumes: `provider.Provider.Stream(ctx, ChatRequest, StreamSink)` + `EventReader` interface（Task 4）
- Produces: `LLM` struct + `NewLLM(provider, model) *LLM` + `Extract(ctx, sessionID) ([]Memory, error)`

- [ ] **Step 1: 写失败测试**

在 `internal/memory/extractor/llm_test.go`：

```go
package extractor

import (
    "context"
    "testing"

    "github.com/Scorpio69t/mengdie-code/internal/memory"
    "github.com/Scorpio69t/mengdie-code/internal/provider"
)

func TestLLMExtractsCandidatesFromStubProvider(t *testing.T) {
    stub := &stubProvider{response: `{"claim":"项目使用 stub 工具","source_type":"agent_message","reason":"inferred"}`}
    reader := &fakeEventReader{rows: []session.EventRow{{Kind: "tool.completed", Name: "edit_file", Success: true}}}
    l := NewLLM(stub, "stub-model", reader)
    got, err := l.Extract(context.Background(), "session-1")
    if err != nil { t.Fatal(err) }
    if len(got) != 1 { t.Fatalf("want 1, got %d", len(got)) }
    if got[0].Claim != "项目使用 stub 工具" { t.Fatalf("claim: %q", got[0].Claim) }
    if got[0].Authority != memory.AuthorityInferred { t.Fatalf("authority: %s", got[0].Authority) }
}

type stubProvider struct{ response string }
func (s *stubProvider) ID() string { return "stub" }
func (s *stubProvider) Capabilities(ctx context.Context, model string) (provider.Capabilities, error) {
    return provider.Capabilities{ToolCalling: true, MaxContextTokens: 4096}, nil
}
func (s *stubProvider) Stream(ctx context.Context, req provider.ChatRequest, sink provider.StreamSink) (*provider.ChatResponse, error) {
    // emit a single text delta with the response, then finish
    _ = sink.OnEvent(ctx, provider.StreamEvent{Kind: provider.StreamTextDelta, Text: s.response})
    return &provider.ChatResponse{
        Message: provider.Message{Role: provider.RoleAssistant, Content: s.response},
    }, nil
}
```

参考 `internal/provider/provider.go:80-89` 确认 `StreamSink` / `StreamEvent` 实际接口。

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory/extractor -run "TestLLM" -count=1`
Expected: FAIL

- [ ] **Step 3: 实现 LLM**

在 `internal/memory/extractor/llm.go`：

```go
package extractor

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "regexp"
    "strings"
    "time"

    "github.com/Scorpio69t/mengdie-code/internal/memory"
    "github.com/Scorpio69t/mengdie-code/internal/provider"
    "github.com/Scorpio69t/mengdie-code/internal/session"
)

// LLM extracts candidate memories by sending the recent event transcript
// to a configured provider and parsing its JSON Lines reply. LLM errors
// (parse, network, schema mismatch) are swallowed — the Extractor contract
// is "produce as many as you can" not "fail the run on no LLM".
type LLM struct {
    provider provider.Provider
    model    string
    reader   EventReader
}

func NewLLM(provider provider.Provider, model string, reader EventReader) *LLM {
    return &LLM{provider: provider, model: model, reader: reader}
}

const llmSystemPrompt = `你是一个 Agent 运行观察者。从给定的运行轨迹中提取 0-5 条候选记忆。
每条输出 JSON {claim, source_type ∈ {user_message, agent_message}, reason}。
claim 必须是项目事实或偏好，不要复述命令。`

const llmMaxEvents = 20

func (l *LLM) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error) {
    if l == nil || l.provider == nil || l.reader == nil { return nil, nil }
    events, err := l.reader.Events(ctx, sessionID, llmMaxEvents)
    if err != nil || len(events) == 0 { return nil, nil }
    raw, err := l.callProvider(ctx, events)
    if err != nil { return nil, nil }  // 失败降级
    return parseLLMResponse(raw), nil
}

func (l *LLM) callProvider(ctx context.Context, events []session.EventRow) (string, error) {
    extCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
    defer cancel()
    transcript := formatTranscript(events)
    req := provider.ChatRequest{
        Model: l.model,
        Messages: []provider.Message{
            {Role: provider.RoleSystem, Content: llmSystemPrompt},
            {Role: provider.RoleUser, Content: transcript},
        },
        Temperature: ptrFloat(0),
        MaxTokens:   512,
    }
    var buf strings.Builder
    sink := &llmSink{buf: &buf}
    _, err := l.provider.Stream(extCtx, req, sink)
    if err != nil { return "", err }
    return buf.String(), nil
}

func ptrFloat(v float64) *float64 { return &v }

type llmSink struct{ buf *strings.Builder }
func (s *llmSink) OnEvent(ctx context.Context, e provider.StreamEvent) error {
    if e.Kind == provider.StreamTextDelta { s.buf.WriteString(e.Text) }
    return nil
}

func formatTranscript(events []session.EventRow) string {
    var b strings.Builder
    for i, e := range events {
        if i >= llmMaxEvents { break }
        status := "ok"
        if !e.Success { status = "fail" }
        fmt.Fprintf(&b, "- %s | %s | %s | %s\n", e.Timestamp.Format("15:04:05"), e.Kind, e.Name, status)
    }
    return b.String()
}

// apiKeyRe matches 20+ char alphanumeric runs (sk-, API keys) for redaction.
var apiKeyRe = regexp.MustCompile(`[A-Za-z0-9_-]{20,}`)

func redact(s string) string { return apiKeyRe.ReplaceAllString(s, "[REDACTED]") }

func parseLLMResponse(raw string) []memory.Memory {
    if raw == "" { return nil }
    var out []memory.Memory
    sc := bufio.NewScanner(strings.NewReader(redact(raw)))
    sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
    for sc.Scan() {
        line := strings.TrimSpace(sc.Text())
        if !strings.HasPrefix(line, "{") { continue }
        var c struct {
            Claim      string `json:"claim"`
            SourceType string `json:"source_type"`
            Reason     string `json:"reason"`
        }
        if err := json.Unmarshal([]byte(line), &c); err != nil { continue }
        if len(c.Claim) < 8 || len(c.Claim) > 200 { continue }
        if c.SourceType != string(memory.SourceTypeUserMessage) &&
           c.SourceType != string(memory.SourceTypeAgentMessage) { continue }
        out = append(out, memory.Memory{
            Claim:     c.Claim,
            Authority: memory.AuthorityInferred,
        })
    }
    return out
}
```

`ptrFloat` 可用 generics 替代（Go 1.18+），但这里 1.26.6 直接用 helper 即可。

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/memory/extractor -run "TestLLM" -count=1 -v`
Expected: PASS

- [ ] **Step 5: 跑全 extractor 测试确认无回归**

Run: `go test -race ./internal/memory/extractor -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/memory/extractor/llm.go internal/memory/extractor/llm_test.go
git commit -m "feat(memory): add LLM extractor with redact + JSON parse"
```

---

### Task 6: Hybrid Extractor

**Files:**
- Create: `internal/memory/extractor/hybrid.go`
- Create: `internal/memory/extractor/hybrid_test.go`

**Interfaces:**
- Consumes: `Rules.Extract` + `LLM.Extract`（Task 3 + 5）
- Produces: `Hybrid` struct + `NewHybrid(rules, llm) *Hybrid` + `Extract(ctx, sessionID)` 合并

- [ ] **Step 1: 写失败测试**

在 `internal/memory/extractor/hybrid_test.go`：

```go
func TestHybridRulesOnlyWhenLLMNil(t *testing.T) {
    rules := &Rules{}  // empty rules, returns nil
    h := NewHybrid(rules, nil)
    got, _ := h.Extract(context.Background(), "session-1")
    if got != nil { t.Fatalf("want nil, got %v", got) }
}

func TestHybridDropsLLMDuplicatesOfRules(t *testing.T) {
    rules := &fakeRules{mems: []memory.Memory{
        {Claim: "项目使用 edit_file 修改文件", Authority: memory.AuthorityRepository},
    }}
    llm := &fakeLLM{mems: []memory.Memory{
        {Claim: "项目使用 edit_file 修改文件", Authority: memory.AuthorityInferred},
        {Claim: "项目偏好中文 README", Authority: memory.AuthorityInferred},
    }}
    h := NewHybrid(rules, llm)
    got, _ := h.Extract(context.Background(), "session-1")
    if len(got) != 2 { t.Fatalf("want 2 (1 rule + 1 unique LLM), got %d", len(got)) }
    var seenEdit, seenReadme bool
    for _, m := range got {
        if m.Claim == "项目使用 edit_file 修改文件" { seenEdit = true }
        if m.Claim == "项目偏好中文 README" { seenReadme = true }
    }
    if !seenEdit || !seenReadme { t.Fatalf("missing claims: edit=%v readme=%v", seenEdit, seenReadme) }
}

type fakeRules struct{ mems []memory.Memory }
func (f *fakeRules) Extract(_ context.Context, _ string) ([]memory.Memory, error) {
    return f.mems, nil
}

type fakeLLM struct{ mems []memory.Memory }
func (f *fakeLLM) Extract(_ context.Context, _ string) ([]memory.Memory, error) {
    return f.mems, nil
}
```

注意：Hybrid 设计接受 `Extractor` interface 作为参数（`Rules` 和 `LLM` 都满足），不需要具体类型。修改 `NewHybrid` 签名：`NewHybrid(rules, llm Extractor) *Hybrid`。这增加测试灵活性。

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory/extractor -run "TestHybrid" -count=1`
Expected: FAIL（`NewHybrid` 不存在）

- [ ] **Step 3: 实现 Hybrid**

在 `internal/memory/extractor/hybrid.go`：

```go
package extractor

import (
    "context"
    "strings"

    "github.com/Scorpio69t/mengdie-code/internal/memory"
    "golang.org/x/text/unicode/norm"
)

// Hybrid composes a rules-based extractor and an LLM-based extractor.
// Rules always run first; LLM is opt-in (nil is allowed). LLM candidates
// that duplicate a rule claim (after Unicode normalization + case-fold)
// are dropped to keep the propose list short.
type Hybrid struct {
    rules Extractor
    llm   Extractor
}

func NewHybrid(rules, llm Extractor) *Hybrid {
    return &Hybrid{rules: rules, llm: llm}
}

func (h *Hybrid) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error) {
    if h == nil { return nil, nil }
    rulesOut, _ := h.rules.Extract(ctx, sessionID)
    if h.llm == nil { return rulesOut, nil }
    llmOut, _ := h.llm.Extract(ctx, sessionID)
    seen := make(map[string]struct{}, len(rulesOut))
    for _, m := range rulesOut { seen[normalizeClaim(m.Claim)] = struct{}{} }
    out := append([]memory.Memory(nil), rulesOut...)
    for _, m := range llmOut {
        if _, dup := seen[normalizeClaim(m.Claim)]; dup { continue }
        out = append(out, m)
    }
    return out, nil
}

func normalizeClaim(s string) string {
    s = strings.ToLower(strings.TrimSpace(s))
    return norm.NFD.String(norm.NFC.String(s))
}
```

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/memory/extractor -run "TestHybrid" -count=1 -v`
Expected: PASS

- [ ] **Step 5: 跑全 extractor 测试确认无回归**

Run: `go test -race ./internal/memory/extractor -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/memory/extractor/hybrid.go internal/memory/extractor/hybrid_test.go
git commit -m "feat(memory): add Hybrid extractor composing rules + LLM"
```

---

### Task 7: Agent.MemoryStore + Agent.MemoryExtractor + applyMemoryExtraction 钩子

**Files:**
- Modify: `internal/agent/runtime.go`（Options / Agent struct / New() / Run end + applyMemoryExtraction 方法）
- Create: `internal/agent/extractor_adapter.go`（`NewExtractorAdapter` + adapter 实现）
- Create: `internal/agent/runtime_extractor_test.go`（新测试；或加到 `runtime_test.go`）

**Interfaces:**
- Consumes: `memory.Store`（Task 4 EventReader + slice 01 Store）+ `memory.Memory` + `EventReader`
- Produces: `agent.MemoryExtractor` interface + `ExtractorAdapter` + `Run` 末尾钩子

- [ ] **Step 1: 写失败测试**

在 `internal/agent/runtime_extractor_test.go`（新建）：

```go
package agent

import (
    "context"
    "errors"
    "testing"
    "time"

    "github.com/Scorpio69t/mengdie-code/internal/memory"
    "github.com/Scorpio69t/mengdie-code/internal/session"
)

func TestRunAppliesExtractionBeforeReturn(t *testing.T) {
    stub := &stubExtractor{mems: []memory.Memory{
        {Claim: "stub rule", Authority: memory.AuthorityRepository},
    }}
    a := newTestAgentWithExtractor(t, stub)
    _, err := a.Run(context.Background(), newTestRequest(), newTestEmitter())
    if err != nil { t.Fatal(err) }
    if stub.callCount != 1 { t.Fatalf("Extract called %d times, want 1", stub.callCount) }
}

func TestRunExtractorFailureDoesNotFailRun(t *testing.T) {
    stub := &stubExtractor{err: errors.New("boom")}
    a := newTestAgentWithExtractor(t, stub)
    result, err := a.Run(context.Background(), newTestRequest(), newTestEmitter())
    if err != nil { t.Fatal(err) }
    if result.Summary == "" { t.Fatal("summary should still be produced") }
}

type stubExtractor struct {
    mems      []memory.Memory
    err       error
    callCount int
}
func (s *stubExtractor) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error) {
    s.callCount++
    return s.mems, s.err
}

// newTestAgentWithExtractor + newTestRequest + newTestEmitter
// 是 test 内的辅助函数，按 agent.Options / memory.Store / emitter 的最简构造
// （不连真实 Provider，用现有 stub Provider）。
```

辅助函数需要构造一个最小可跑 Agent：参考 `internal/agent/runtime_test.go` 已有的测试 helper（如果存在 `newTestAgent` / `newTestRequest`），直接复用；否则按 Options 构造一个新 Agent（Options.Provider 用现有 `provider.Stub` 或 fake，registry 用 `tools.NewRegistry(tools.DefaultTools()...)` 等）。

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/agent -run "TestRunAppliesExtraction|TestRunExtractorFailure" -count=1`
Expected: FAIL

- [ ] **Step 3: 实现 adapter + Options 字段 + applyMemoryExtraction**

在 `internal/agent/extractor_adapter.go`：

```go
package agent

import (
    "context"

    "github.com/Scorpio69t/mengdie-code/internal/memory"
)

// ExtractorAdapter wires a memory.extractor into the agent.MemoryExtractor
// surface. The Extractor is shared across Agent instances and is safe
// for concurrent calls; the adapter is stateless.
type ExtractorAdapter struct {
    ext      memoryExtractor
    store    *memory.Store
    projID   string
}

func NewExtractorAdapter(ext memoryExtractor, store *memory.Store, projectIdentity string) *ExtractorAdapter {
    return &ExtractorAdapter{ext: ext, store: store, projID: projectIdentity}
}

func (a *ExtractorAdapter) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error) {
    return a.ext.Extract(ctx, sessionID)
}
```

注：`memoryExtractor` 是 `agent` 包内的一个内部 interface，等价于 `*memory/extractor.Extractor` 但避免 import 循环。改 `runtime.go` 顶部新增：

```go
// memoryExtractor is the local surface the agent package needs; the
// production wiring (NewExtractorAdapter) wraps the real implementation.
type memoryExtractor interface {
    Extract(ctx context.Context, sessionID string) ([]memory.Memory, error)
}
```

在 `internal/agent/runtime.go` 改 `MemoryExtractor` 字段类型（保持 interface 名字 `MemoryExtractor`，签名 `Extract(ctx, sessionID) ([]memory.Memory, error)`）：

```go
type Options struct {
    // ... 既有字段 ...
    MemoryStore     *memory.Store      // NEW
    MemoryExtractor MemoryExtractor    // NEW (签名不变)
}
type Agent struct {
    // ... 既有字段 ...
    memoryStore     *memory.Store       // NEW
    memoryExtractor MemoryExtractor     // NEW
}
```

改 `New()` 初始化：
```go
return &Agent{
    // ... 既有字段 ...
    memoryStore:     options.MemoryStore,
    memoryExtractor: options.MemoryExtractor,
}, nil
```

在 `Agent.Run` 末尾（最后 `return state.result(summary), nil` 之前）加：
```go
a.applyMemoryExtraction(ctx, request)
return state.result(summary), nil
```

新增方法：
```go
// applyMemoryExtraction runs the configured MemoryExtractor at the end of
// the run and proposes the candidates to the memory Store. Extract failure
// is logged as a warning and never fails the run; per-row ProposeMemory
// failures are logged and skipped. Number of candidates is capped at 5
// to bound the propose-time size of the private context log.
func (a *Agent) applyMemoryExtraction(ctx context.Context, request RunRequest) {
    if a.memoryExtractor == nil || a.memoryStore == nil || a.projectIdentity == "" {
        return
    }
    extCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
    defer cancel()
    candidates, err := a.memoryExtractor.Extract(extCtx, request.RunID)
    if err != nil {
        a.warnExtraction(ctx, "memory_extractor_failed", err)
        return
    }
    if len(candidates) == 0 { return }
    if len(candidates) > 5 { candidates = candidates[:5] }
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
        if _, err := a.memoryStore.ProposeMemory(extCtx, mem); err != nil {
            a.warnExtraction(ctx, "memory_extractor_propose_failed", err)
        }
    }
}

func (a *Agent) warnExtraction(ctx context.Context, code string, err error) {
    // 占位实现：slice 03 在 a.emitter + events.KindWarning 真正可写时再启用
}
```

注：当前 slice 02 不引入 emitter / events 依赖；用 `warnExtraction` 占位避免 coupling；后续 slice 03 在 agent 包内已经引了 `events` 后改成 emit warning。

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/agent -run "TestRunAppliesExtraction|TestRunExtractorFailure" -count=1`
Expected: PASS

- [ ] **Step 5: 跑全 agent 测试确认无回归**

Run: `go test -race ./internal/agent -count=1`
Expected: 全 PASS（slice 01 的 19 个测试 + 新 2 个 = 21）

- [ ] **Step 6: Commit**

```bash
git add internal/agent/runtime.go internal/agent/extractor_adapter.go internal/agent/runtime_extractor_test.go
git commit -m "feat(agent): integrate MemoryExtractor hook at end of Run"
```

---

### Task 8: app.Runtime 拼装（retriever + extractor + tools）

**Files:**
- Modify: `internal/app/runtime.go`（`runAgent` 函数中构造 store/retriever/extractor + tools.DefaultTools options + Agent.Options 注入）
- Modify: 任何其他调用 `tools.DefaultTools()` 的地方（`cmd/mengdie/main.go` 等）

**Interfaces:**
- Consumes: `*memory.Store`, `*memory.Retriever`, `*memory/extractor.Hybrid`, `loaded.ProjectIdentityValue()`, 既有 `tools.DefaultTools` 签名
- Produces: 拼装好的 `*Agent` 携带 `MemoryStore`/`MemoryExtractor`/`ProjectIdentity` 字段 + 注册 `memory_recall` 的工具列表

- [ ] **Step 1: 写失败测试**

在 `internal/app/runtime_extractor_test.go`（新建）：

```go
func TestRunAgentRegistersMemoryRecallTool(t *testing.T) {
    state := setupAppTestStateForExtractor(t)
    code := runApp(state, []string{"--json", "memory", "tools"})
    if code != 0 { t.Fatalf("run exit %d", code) }
    if !strings.Contains(state.stdout.String(), "memory_recall") {
        t.Fatalf("memory_recall tool not in default registry; got: %s", state.stdout.String())
    }
}
```

如果 `setupAppTestStateForExtractor` / `runApp` 在 slice 01 测试中已有，直接复用；否则按 `internal/app/memory_test.go` 的 helper 风格新建一个 minimal state。

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/app -run "TestRunAgentRegistersMemoryRecallTool" -count=1`
Expected: FAIL（`runApp` 不接受 `memory tools` 子命令，或输出不包含 `memory_recall`）

- [ ] **Step 3: 实现 app.Runtime 拼装**

在 `internal/app/runtime.go` 的 `runAgent` 函数中（或其他构建 Agent.Options 的位置），找到现有：

```go
registeredTools := append(tools.DefaultTools(), contextSourceTool)
```

替换为：

```go
sessionStore, _ := session.OpenSQLite(ctx, session.OpenOptions{...})  // 已有
memoryStore := memory.OpenMemory(sessionStore)
retriever := memory.NewRetriever(memoryStore)
retrieverAdapter := agent.NewRetrieverAdapter(retriever, loaded.ProjectIdentityValue())  // Task 7 ship 时已存在
hybridExtractor := memoryextractor.NewHybrid(memoryextractor.NewRules(memoryStore, sessionStore), nil)  // v0.1 LLM 端 nil
extractorAdapter := agent.NewExtractorAdapter(hybridExtractor, memoryStore, loaded.ProjectIdentityValue())

registeredTools := append(
    tools.DefaultTools(
        tools.WithMemoryRetriever(retrieverAdapter),
        tools.WithProjectIdentity(loaded.ProjectIdentityValue()),
    ),
    contextSourceTool,
)
```

并在 Agent.Options 加：
```go
MemoryStore:     memoryStore,
MemoryExtractor: extractorAdapter,
```

注：`memoryextractor.NewRules(memoryStore, sessionStore)` 的签名是 `NewRules(store, eventReader)`。但 slice 02 Task 4 改成 `NewRules(reader EventReader)` 接受 EventReader。如果这里 `memoryStore` 不直接实现 `EventReader`，需要 `sessionStore` 来取 events。**约定：`memory.Store` 不直接实现 `EventReader`；用 `sessionStore` 作为 `EventReader`**。Task 4 已经有这个定义。

更新：
```go
hybridExtractor := memoryextractor.NewHybrid(
    memoryextractor.NewRules(sessionStore),  // sessionStore 实现 EventReader
    nil,                                       // v0.1 不带 LLM
)
```

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/app -run "TestRunAgentRegistersMemoryRecallTool" -count=1`
Expected: PASS

- [ ] **Step 5: 跑全 app 测试确认无回归**

Run: `go test -race ./internal/app -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/app/runtime.go
git commit -m "feat(app): wire memory_extractor + memory_recall in runAgent"
```

---

### Task 9: Trust Set 5 个 inferred_extraction 场景 + runner 扩展

**Files:**
- Modify: `evals/memory/trust-set-v1.json`（新增 5 个场景）
- Modify: `internal/memory/trustset/runner.go`（支持 `actions[].type = "extract"` + 验证 extracted_memories）
- Modify: `internal/memory/trustset/runner_test.go`（加测试或更新现有）

**Interfaces:**
- Consumes: 既有 Trust Set JSON schema；`*memory.Store.List(scope=project, status=proposed)`
- Produces: 5 个新场景 JSON + runner 验证逻辑

- [ ] **Step 1: 写失败测试**

在 `internal/memory/trustset/runner_test.go` 追加：

```go
func TestRunnerHandlesExtractScenario(t *testing.T) {
    manifestPath := locateManifest(t)
    scenarios := loadScenarios(t, manifestPath)
    var extract *Scenario
    for i := range scenarios {
        if scenarios[i].ID == "extractor-rules-edits" {
            extract = &scenarios[i]
            break
        }
    }
    if extract == nil { t.Fatal("extractor-rules-edits scenario not found in trust-set-v1.json") }
    store := openStoreForTrustSet(t)
    store2 := memory.OpenMemory(store)
    report, _ := Run(context.Background(), store2, []Scenario{*extract}, "")
    if len(report.Scenarios) == 0 { t.Fatal("no scenario results") }
    if !report.Scenarios[0].Passed { t.Fatalf("scenario failed: %s", report.Scenarios[0].Reason) }
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory/trustset -run "TestRunnerHandlesExtractScenario" -count=1`
Expected: FAIL（场景不存在或 runner 不支持 extract action）

- [ ] **Step 3: 在 trust-set-v1.json 新增 5 个场景**

在 `evals/memory/trust-set-v1.json` 末尾 `tasks` 数组添加 5 个对象。`extractor-rules-edits` 示例：

```json
{
  "id": "extractor-rules-edits",
  "category": "inferred",
  "description": "规则抽取器从 edit_file 工具成功事件中提取记忆",
  "setup": {
    "seed_events": [
      {"kind": "tool.completed", "name": "edit_file", "success": true, "timestamp_offset": "-1m"}
    ]
  },
  "actions": [
    {"type": "run_run", "max_turns": 1},
    {"type": "extract", "scope": "project/mengdie", "expect_proposed_count_gte": 1}
  ],
  "expected": {
    "extracted_memories": [
      {"claim_contains": "edit_file", "authority": "inferred", "status": "proposed"}
    ]
  }
}
```

类似地写 `extractor-rules-tests`（ruleGoTest, source_type=verified）、`extractor-rules-lint`（ruleGoLint, verified）、`extractor-llm-tool-pref`（LLM 提取, 需 stub Provider 返回 JSON 候选, authority=inferred）、`extractor-hybrid-both`（规则 + LLM 都有产物，LLM 候选不与规则重复）。

`extract` action 是新动词：在 runner 里实现为 `runner` 把"已发生的事件"跑过 Extractor 一次，然后 `Store.List(scope=project, status=proposed)` 读候选，按 `expected.extracted_memories[]` 模糊匹配 `claim_contains`。

- [ ] **Step 4: 在 runner.go 实现 extract action**

在 `internal/memory/trustset/runner.go` 的 `runOne` 或 `dispatch` 函数中加 `extract` action 处理：

```go
case "extract":
    // 触发 Extractor（rules + 也许 LLM）
    candidates, _ := store.Extract(ctx, s.ID)  // 需要 Extractor 接口被 Store 暴露，或在 runner 里临时构造
    // candidates 已经在 Store 里了（被 ProposeMemory 写入），所以这一步只验证
    list, _ := store.List(ctx, memory.ListQuery{
        ScopeKind: extractScopeKind(a.Scope), ScopeValue: extractScopeValue(a.Scope),
        Status: "proposed", Limit: 50,
    })
    return validateExtractedMemories(list, a.ExpectProposedCountGTE, expected)
```

- [ ] **Step 5: 跑测试确认 pass**

Run: `go test ./internal/memory/trustset -count=1 -v`
Expected: 30 + 5 场景全 PASS（slice 01 旧 30 不退化 + 新 5 通过）

- [ ] **Step 6: 跑全 memory + memory/trustset 测试确认无回归**

Run: `go test -race ./internal/memory/... -count=1`
Expected: 全 PASS

- [ ] **Step 7: Commit**

```bash
git add evals/memory/trust-set-v1.json internal/memory/trustset/runner.go internal/memory/trustset/runner_test.go
git commit -m "test(memory): add 5 inferred_extraction Trust Set scenarios + runner support"
```

---

### Task 10: Live provider 端到端测试 + CI 集成 + 文档

**Files:**
- Create: `internal/memory/extractor/live_provider_test.go`（`//go:build liveprovider`）
- Modify: `.github/workflows/ci.yml`（quality job 加 `go test -race ./internal/memory/extractor/...`）
- Modify: `README.md`（勾选 M3 Slice 02 + 新增 M3 Slice 03/04 占位）
- Create: `docs/development/phase-3-slice-02/IMPLEMENTATION_REPORT.md`

**Interfaces:**
- Consumes: 既有 live provider env var 守则（MENGDIE_LIVE_SMOKE、MENGDIE_LIVE_*）+ slice 01 live provider 模板
- Produces: live test + CI 更新 + 文档

- [ ] **Step 1: 写 live provider test**

在 `internal/memory/extractor/live_provider_test.go`：

```go
//go:build liveprovider

package extractor

import (
    "context"
    "encoding/json"
    "os"
    "path/filepath"
    "runtime"
    "testing"
    "time"

    "github.com/Scorpio69t/mengdie-code/internal/memory"
    "github.com/Scorpio69t/mengdie-code/internal/provider"
    "github.com/Scorpio69t/mengdie-code/internal/provider/openaicompat"
    "github.com/Scorpio69t/mengdie-code/internal/session"
)

func TestLiveProviderMemoryExtractorEndToEnd(t *testing.T) {
    if os.Getenv("MENGDIE_LIVE_SMOKE") != "1" { t.Skip("set MENGDIE_LIVE_SMOKE=1") }
    baseURL := requiredEnv(t, "MENGDIE_LIVE_BASE_URL")
    apiKey := requiredEnv(t, "MENGDIE_LIVE_API_KEY")
    model := requiredEnv(t, "MENGDIE_LIVE_MODEL")
    dataDir := t.TempDir()
    projectRoot := t.TempDir()
    store, _ := session.OpenSQLite(context.Background(), session.OpenOptions{
        DataDir: dataDir, ProjectRoot: projectRoot, Now: time.Now,
    })
    defer store.Close()
    memStore := memory.OpenMemory(store)
    client, _ := openaicompat.New(openaicompat.Config{BaseURL: baseURL, APIKey: apiKey})
    reader := store  // sessionStore 实现 EventReader
    l := NewLLM(client, model, reader)
    got, err := l.Extract(context.Background(), "live-test")
    if err != nil { t.Fatal(err) }
    evidence := map[string]any{
        "suite_id": "memory-extractor-live-v1", "platform_os": runtime.GOOS,
        "provider_url": maskURL(baseURL), "model": model,
        "scenario": "live_test", "hit_count": len(got), "passed": true,
        "started_at": time.Now().UTC().Format(time.RFC3339Nano),
    }
    out := filepath.Join("internal", "memory", "extractor", "evidence",
        "live-"+runtime.GOOS+"-"+time.Now().Format("20060102")+".json")
    data, _ := json.MarshalIndent(evidence, "", "  ")
    if err := os.WriteFile(out, data, 0o600); err != nil { t.Fatal(err) }
}

func requiredEnv(t *testing.T, name string) string { /* 复用 slice 01 模板 */ }
func maskURL(u string) string { /* 复用 slice 01 模板 */ }
```

参考 `internal/memory/live_provider_test.go`（Task 12）作为模板。

- [ ] **Step 2: 跑测试（无 env 时）**

Run: `go test -tags=liveprovider -run TestLiveProviderMemoryExtractorEndToEnd ./internal/memory/extractor -count=1`
Expected: SKIP

- [ ] **Step 3: 更新 .github/workflows/ci.yml**

在 `.github/workflows/ci.yml` 的 `quality` job 中，`运行 Memory Trust Set` 步骤后加：

```yaml
      - name: 运行 Memory Extractor
        run: go test -race ./internal/memory/extractor/... -count=1
```

- [ ] **Step 4: 跑全 CI 步骤本地验证**

Run:
```bash
gofmt -l .        # 0 output
go vet ./...
go test -race ./...
golangci-lint run ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./cmd/...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./cmd/...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/...
go test -race ./internal/memory/extractor/... -count=1
```

Expected: 除 Windows pre-existing `TestShellExecute...` 外全 PASS

- [ ] **Step 5: 更新 README.md**

读 `README.md` 第 107-109 行（slice 01 添加的 M3 行附近），改成：

```markdown
- [x] 第三阶段 Slice 02：MemoryExtractor 自动候选提取 + memory_recall 工具注册（[设计稿](./docs/superpowers/specs/2026-08-24-m3-slice-02-extractor-design.md)、[实施报告](./docs/development/phase-3-slice-02/IMPLEMENTATION_REPORT.md)）
- [ ] M3 Slice 03：自动 Approve 高频低风险候选（待办）
- [ ] M3 Slice 04：跨 Authority dispute 标记（待办）
- [ ] M4：默认只生成提案的复盘机制
```

- [ ] **Step 6: 写实施报告**

在 `docs/development/phase-3-slice-02/IMPLEMENTATION_REPORT.md` 写一份报告（参考 `docs/development/phase-3-slice-01/IMPLEMENTATION_REPORT.md` 风格）：
- 交付范围
- 关键设计与守则（hybrid 顺序、5s 超时、≤5 条、ProposeMemory 而非 SaveUserMemory）
- 质量门禁
- Follow-up（slice 01 的 5 个 + slice 02 新发现的）
- 红线检查

- [ ] **Step 7: Commit**

```bash
git add internal/memory/extractor/live_provider_test.go .github/workflows/ci.yml README.md docs/development/phase-3-slice-02/
git commit -m "test(memory): add live provider extractor test + CI + docs"
```

---

## Execution Order

| Order | Task | Output |
|---|---|---|
| 1 | Task 1: ProjectIdentity 字段 + 方法 | `Loaded.ProjectIdentity` + `ProjectIdentityValue()` + 3 测试 |
| 2 | Task 2: DefaultTools 签名扩展 | `DefaultToolsOption` + `WithMemoryRetriever` + `WithProjectIdentity` + 2 测试 |
| 3 | Task 3: Extractor interface + Rules | `Extractor` interface + `Rules` + 6 unit 测试 |
| 4 | Task 4: EventReader interface + 重构 loadEvents | `EventReader` + `session.Store.Events` + 2 测试 |
| 5 | Task 5: LLM Extractor | `LLM` + redact + JSON parse + 1 测试 |
| 6 | Task 6: Hybrid Extractor | `Hybrid` + 2 测试 |
| 7 | Task 7: Agent.MemoryStore + MemoryExtractor + applyMemoryExtraction | `agent.MemoryExtractor` + `ExtractorAdapter` + 2 测试 |
| 8 | Task 8: app.Runtime 拼装 | `runAgent` 注入 + 1 测试 |
| 9 | Task 9: Trust Set 5 场景 + runner | 30+5 场景 + 1 测试 |
| 10 | Task 10: live provider + CI + docs | live test + CI 步骤 + README + 报告 |

## Final Gates

```bash
gofmt -l .                              # must be empty
go vet ./...                           # clean
go test -race ./...                    # 除 Windows pre-existing TestShellExecute 外全 PASS
golangci-lint run ./...                # 0 issue
govulncheck@v1.1.4 ./...               # no vulns
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64  go build ./cmd/...  # OK
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64  go build ./cmd/...  # OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/...  # OK
CGO_ENABLED=0 GOOS=linux GOARCH=amd64   go build ./cmd/...  # OK
go test -race ./internal/memory/... -run TestMemoryTrustSetV1   # 30+5 场景全过
go test -tags=liveprovider -run TestLiveProviderMemoryExtractorEndToEnd ./internal/memory/extractor/  # SKIP 无 env；有 env 时跑 + 写 evidence
```

## Beads Close

CI 全过、用户审核后用：

```bash
bd close mengdie-6gd --reason="M3 Slice 02 完成：10 个 task 全部 ship，30+5 Trust Set 场景全过，live provider extractor 测试就绪，hybrid 顺序 + 5s 超时 + ≤5 条 + ProposeMemory 守则全过，PR ready-for-review"
```
