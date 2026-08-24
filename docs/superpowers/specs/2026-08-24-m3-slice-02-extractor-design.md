# M3 Slice 02 设计稿：MemoryExtractor 自动候选提取 + memory_recall 工具注册

> 状态：已通过 7 节分段确认，待 user 审核后转入 writing-plans。
> 日期：2026-08-24
> Beads：`mengdie-6gd`（M3 Slice 02）
> Spec 关联：M3 Slice 01 `docs/superpowers/specs/2026-08-24-m3-slice-01-trusted-memory-design.md`

## 1. 背景

M3 Slice 01 已落地可信记忆系统最小可用层（schema + FTS5 + 显式 CLI + Agent 第一 turn 注入 + Trust Set 30 场景 5 指标 baseline）。但 M3 的「任务结束候选提取」是 spec §6.2 明确列出的 M3 交付项，slice 01 只定义了 `MemoryExtractor` 接口占位、没接 Run 钩子、没实现自动 propose。

M3 Slice 02 落地三件切片 01 留下的 follow-up：
- `MemoryExtractor` 接口 + 规则/LLM/混合三实现 + Agent.Run 钩子
- `ProjectIdentity` 字段在 `config.Loaded` 派生并透传到 `agent.Options`
- `app.Runtime` 把 `*memory.Retriever` + `*memory.Extractor` 适配并注册 `memory_recall` 工具

## 2. 范围与不在范围

### 2.1 范围内

- `internal/memory/extractor/extractor.go`：`MemoryExtractor` interface + 包文档
- `internal/memory/extractor/rules.go`：规则实现
- `internal/memory/extractor/llm.go`：LLM 实现
- `internal/memory/extractor/hybrid.go`：组合实现
- `internal/memory/extractor/{rules,llm,hybrid,runner}_test.go`
- `internal/agent/extractor_adapter.go`：`*memory.Extractor` → `agent.MemoryExtractor` 适配
- `internal/agent/runtime.go`：`Options.MemoryExtractor` 字段；`Options.MemoryStore` 字段；`Agent.Run` 末尾 `applyMemoryExtraction` 钩子
- `internal/config/config.go`：`Loaded.ProjectIdentity` 字段 + `ProjectIdentityValue()` 方法
- `internal/tools/defaults.go`：`DefaultTools(opts ...DefaultToolsOption) []Tool` + `WithMemoryRetriever` / `WithProjectIdentity` 函子
- `internal/app/runtime.go`：retriever + extractor 适配、`tools.DefaultTools(...)` 拼装
- `evals/memory/trust-set-v1.json`：5 个 `inferred_extraction` 增量场景
- `internal/memory/extractor/live_provider_test.go`：`//go:build liveprovider` 端到端
- `.github/workflows/ci.yml`：quality job 加 `internal/memory/extractor/...` 步骤
- `README.md`：勾选 M3 Slice 02

### 2.2 不在范围内

- 跨 Authority dispute 标记（spec §4.2 row 3）—— 仍 deferred
- 嵌入向量检索（arch §8.7）—— v0.1 仍纯 FTS5
- Reflect / Consolidate（arch §9）—— 属 M4 切片
- 跨项目记忆复制 —— arch §17 follow-up
- 任务执行中实时抽取（中途抽取可能干扰 agent 推理；只在 Run 结束后批抽）
- LLM 提取走 `SaveUserMemory`（违反 slice 01 守则：inferred 必须经 `Approve` 才 active）
- 自动 Approve LLM 候选（人类审查不可替代）

## 3. 接口定义

### 3.1 `internal/memory/extractor/extractor.go`

```go
type Extractor interface {
    Extract(ctx context.Context, sessionID string) ([]Memory, error)
}
```

返回 `[]Memory` 而非已写入的 ID —— `Agent.Run` 拿到结果后调 `memory.Store.ProposeMemory` 走统一审计路径。

### 3.2 `internal/agent/runtime.go`（扩展）

```go
type MemoryExtractor interface {
    Extract(ctx context.Context, sessionID string) ([]memory.Memory, error)
}
type Options struct {
    // ... 既有字段 ...
    MemoryRetriever  MemoryRetriever
    ProjectIdentity  string
    MemoryExtractor  MemoryExtractor  // NEW
    MemoryStore      *memory.Store     // NEW — app.Runtime 注入
}
```

`MemoryExtractor` interface 不放在 `agent` 包内（避免循环依赖：`agent → memory → session` 已存在；`memory` 不依赖 `agent`）。Adapter 在 `internal/agent/extractor_adapter.go` 内做薄映射。

## 4. 规则实现（`rules.go`）

从 `*memory.Store` 拿到 session 全部 events（`sessionID` 查 View），按规则表生成候选：

| Trigger event | 候选 claim | Authority |
|---|---|---|
| `tool.completed` kind=`edit_file` success ≥ 1 次 | `项目使用 edit_file 修改文件` | `repository` |
| `tool.completed` kind=`write_file` success ≥ 1 次 | `项目使用 write_file 创建或覆盖文件` | `repository` |
| `tool.completed` kind=`shell` 命令是 `go test ./...` | `项目测试入口是 go test ./...` | `verified` |
| `tool.completed` kind=`shell` 命令是 `golangci-lint` | `项目使用 golangci-lint 做静态检查` | `verified` |
| `run.completed` + 全部 tool success | `本次 Agent Run 整体成功` | `inferred` |
| `run.failed` category=`provider_protocol` ≥ 2 次 | `Provider 协议层不稳定` | `inferred` |

每条规则产出 1 个 `Memory`，Authority 路由由事件类型定。多个相同 claim 走 Store 的 idempotency 去重。

`RulesExtractor.Extract(ctx, sessionID)` 通过 `memory.Store.List(ctx, ListQuery{ScopeKind: "project", ScopeValue: "*"})` 或类似 query 拉 events，再做时间窗 + 计数判断。

## 5. LLM 实现（`llm.go`）

**输入准备：**
- 最近 N=20 条 session events（`sessionID` 查 View）
- 脱敏：去掉 payload 里的 API key 模式（`[A-Za-z0-9_-]{20,}`、URL query string、用户代码）
- 格式化为 prompt-friendly 文本

**Provider：**
- 接受 `provider.Provider` 接口（与 agent 复用）
- `temperature=0`，`max_tokens=512`
- 系统 prompt：

```
你是一个 Agent 运行观察者。从给定的运行轨迹中提取 0-5 条候选记忆。
每条输出 JSON {claim, source_type ∈ {user_message, agent_message}, reason}。
claim 必须是项目事实或偏好，不要复述命令。
```

**输出解析：**
- 读 stdout，解析 JSON Lines
- filter：`claim` 长度 < 8 或 > 200 丢弃、`source_type` 不在白名单丢弃
- 失败模式：解析错误 / 网络错误 → 返回 `(nil, nil)`，不阻断 Run

**实现：**
```go
func NewLLM(provider provider.Provider, model string) *LLM { ... }
func (l *LLM) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error) {
    events := l.loadAndRedactEvents(ctx, sessionID)
    if len(events) == 0 { return nil, nil }
    candidates, err := l.callProvider(ctx, events)
    if err != nil { return nil, nil }  // 不阻断
    return candidates, nil
}
```

## 6. 混合实现（`hybrid.go`）

```go
func NewHybrid(rules *Rules, llm *LLM) *Hybrid { ... }
func (h *Hybrid) Extract(ctx, sessionID) ([]Memory, error) {
    rules, _ := h.rules.Extract(ctx, sessionID)
    rulesClaims := claimSet(rules)
    var llm []Memory
    if h.llm != nil {
        llm, _ = h.llm.Extract(ctx, sessionID)
    }
    // 丢弃 LLM 候选中与 rules claim 规范化的重叠
    filtered := make([]Memory, 0, len(llm))
    for _, m := range llm {
        if _, dup := rulesClaims[normalize(m.Claim)]; dup { continue }
        filtered = append(filtered, m)
    }
    // 合并
    return append(rules, filtered...), nil
}
```

LLM 端可选（`llm == nil` 时退化为纯规则）。`normalize` 用 `strings.EqualFold` + NFD/NFC 规范化（与 Store.Save 的 idempotency 路径一致）。

## 7. Agent 集成（`internal/agent/runtime.go` + `extractor_adapter.go`）

### 7.1 Hook 位置

`Agent.Run` 在 `for state.Turn < request.MaxTurns` 循环之后、最终返回之前：

```go
a.applyMemoryExtraction(ctx, request)

return state.result(summary), nil
```

### 7.2 `applyMemoryExtraction` 实现

```go
func (a *Agent) applyMemoryExtraction(ctx context.Context, request RunRequest) {
    if a.memoryExtractor == nil || a.memoryStore == nil || a.projectIdentity == "" {
        return
    }
    extCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
    defer cancel()
    candidates, err := a.memoryExtractor.Extract(extCtx, request.RunID)
    if err != nil || len(candidates) == 0 { return }
    if len(candidates) > 5 { candidates = candidates[:5] }  // 防失控
    for _, mem := range candidates {
        if mem.Scope.Value == "" {
            mem.Scope = memory.Scope{Kind: "project", Value: a.projectIdentity}
        }
        if mem.Source.Ref == "" {
            mem.Source = memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: request.RunID + ":extractor"}
        }
        _, err := a.memoryStore.ProposeMemory(extCtx, mem)
        if err != nil {
            _, _ = a.emitter.Emit(extCtx, events.KindWarning, events.Warning{
                Code:    "memory_extractor_propose_failed",
                Message: fmt.Sprintf("propose failed: %v", err),
            })
        }
    }
}
```

### 7.3 安全闸

- Extractor 只读 `*memory.Store` 和 session events；不接触 `state.Messages`、emitter、broker
- `context.WithoutCancel` 避免上游 cancel 影响 propose
- 走 `ProposeMemory`（inferred）→ 必须经 `Approve` 才 active（slice 01 守则）
- 数量限制 ≤ 5 防 context 注入失控
- 失败降级：单条 ProposeMemory 失败 → emit warning + 继续下一条；Extract 整体失败 → 静默返回

## 8. ProjectIdentity（`internal/config/config.go`）

```go
type Loaded struct {
    // ... 既有字段 ...
    ProjectIdentity string  // 显式设置时优先；空时由方法 fallback
}
func (l Loaded) ProjectIdentityValue() string {
    if l.ProjectIdentity != "" {
        return l.ProjectIdentity
    }
    return filepath.Base(strings.TrimRight(l.ProjectRoot, string(os.PathSeparator)))
}
```

L 字段保持不可变；fallback 在方法中计算。`filepath.Base` 在跨平台下一致。

## 9. DefaultTools 签名扩展（`internal/tools/defaults.go`）

```go
type DefaultToolsOption func(*defaultToolsConfig)
type defaultToolsConfig struct {
    memoryRetriever MemoryRecallRetriever
    projectIdentity  string
}
func WithMemoryRetriever(r MemoryRecallRetriever) DefaultToolsOption {
    return func(c *defaultToolsConfig) { c.memoryRetriever = r }
}
func WithProjectIdentity(id string) DefaultToolsOption {
    return func(c *defaultToolsConfig) { c.projectIdentity = strings.TrimSpace(id) }
}
func DefaultTools(opts ...DefaultToolsOption) []Tool {
    cfg := defaultToolsConfig{}
    for _, o := range opts { o(&cfg) }
    tools := []Tool{ /* 既有 M1 set */ }
    if cfg.memoryRetriever != nil {
        tools = append(tools, NewMemoryRecallTool(cfg.memoryRetriever, WithProjectIdentity(cfg.projectIdentity)))
    }
    return tools
}
```

兼容：零 options 时行为同 slice 01（不附加 `memory_recall` 工具）。`memory_recall` 工具内已支持 `WithProjectIdentity` option（Task 8 ship 时预留，规格一致）。

## 10. app.Runtime 拼装（`internal/app/runtime.go`）

```go
// 替换原 registeredTools := append(tools.DefaultTools(), contextSourceTool)
memoryStore := memory.OpenMemory(sessionStore)
retriever := memory.NewRetriever(memoryStore)
retrieverAdapter := agent.NewRetrieverAdapter(retriever, loaded.ProjectIdentityValue())
hybrid := memoryextractor.NewHybrid(memoryextractor.NewRules(), nil) // v0.1 先纯规则
extractorAdapter := agent.NewExtractorAdapter(hybrid, loaded.ProjectIdentityValue())

registeredTools := append(
    tools.DefaultTools(
        tools.WithMemoryRetriever(retrieverAdapter),
        tools.WithProjectIdentity(loaded.ProjectIdentityValue()),
    ),
    contextSourceTool,
)

// Agent.Options 同时注入 MemoryStore + MemoryExtractor + ProjectIdentity
```

## 11. Trust Set 扩展（`evals/memory/trust-set-v1.json`）

5 个 `inferred_extraction` 增量场景（v0.1 增量；slice 01 的 30 个场景不变）：

| ID | 期望产物 |
|---|---|
| `extractor-rules-edits` | 1 条 `项目使用 edit_file` 候选（authority=repository, status=proposed） |
| `extractor-rules-tests` | 1 条 `项目测试入口是 go test ./...`（authority=verified） |
| `extractor-rules-lint` | 1 条 `项目使用 golangci-lint`（authority=verified） |
| `extractor-llm-tool-pref` | ≥ 1 条偏好（authority=inferred, status=proposed, stub Provider） |
| `extractor-hybrid-both` | 规则 + LLM 都有产物；LLM 候选不与规则重复 |

Trust Set runner 扩展：`actions[].type` 支持 `extract`，`expected.extracted_memories[]` 列表，每条匹配 `claim` / `authority` / `status`（不强制 `memory_present=true` —— extracted 是 propose，未必落地）。runner 跑 `memory.Store.List(scope=project, status=proposed)` 并按 claim 模糊匹配。

## 12. 质量门禁

- `go fmt ./...` 通过
- `go vet ./...` 无问题
- `go test -race ./...` 除 Windows pre-existing `TestShellExecute...` 外全过
- `golangci-lint run ./...` 0 issue
- `govulncheck@v1.1.4 ./...` No vulnerabilities
- `CGO_ENABLED=0 go build ./cmd/...` 四目标：darwin-arm64、darwin-amd64、windows-amd64、linux-amd64 全过
- `go test -race ./internal/memory/... ./internal/memory/extractor/... ./internal/agent/... ./internal/app/... ./internal/tools/...` 全过
- `go test -race ./internal/memory/... -run TestMemoryTrustSetV1` 30+5 场景通过（slice 01 旧指标 + 增量场景不退化）
- `go test -tags=liveprovider -run TestLiveProviderMemoryExtractorEndToEnd ./internal/memory/extractor/...`：env 满足时跑真实 Provider；evidence 落 `internal/memory/extractor/evidence/live-{os}.json`，API Key redaction

## 13. 风险与回滚

| 风险 | 缓解 |
|---|---|
| LLM 提取不准确（幻觉/抓取不到关键事实） | Trust Set 验证 5 场景；规则 + LLM 组合；LLM 必须走 ProposeMemory 需 Approve；失败静默 |
| Extract 阶段拖慢 Run | 30s 超时；Extract 错误静默返回；Run 不阻塞 |
| 项目改名导致 ProjectIdentity 漂移 | Loaded.ProjectIdentity 显式字段优先；fallback 只在 ProjectRoot 设置后稳定 |
| Agent 拿不到 memory_store | Agent.MemoryStore 字段在 app.Runtime 注入；测试用 stub store |
| 跨 Slice 02 改动 `agent.Options` 影响 slice 01 测试 | slice 01 测试不传 MemoryStore / MemoryExtractor 时为 nil，hook 自动跳过 |
| 已有 `tools.DefaultTools()` 三处调用点 | 调用签名改为 variadic options，老调用 `DefaultTools()` 仍编译运行 |

## 14. 不在范围内的明确条目

- 跨 Authority dispute 标记（spec §4.2 row 3）
- 嵌入向量检索（arch §8.7）
- Reflect / Consolidate（arch §9，M4 切片）
- 跨项目记忆复制
- 任务执行中实时抽取
- LLM 走 `SaveUserMemory`（违反 slice 01 守则）
- 自动 Approve LLM 候选（人类审查不可替代）
- Slice 01 的 5 个 follow-up（pending approval 状态推断、Supersede 静默覆盖链、cross-scope exit 5、CLI thin test surface、`memory_recall` 工具未在 app.Runtime 注册的旧实现 —— 本切片已解决）

## 15. Follow-up Beads 候选

- M3 Slice 03：自动 Approve 高频低风险候选（如 `项目使用 edit_file` 这种 fingerprint 类）
- M3 Slice 04：跨 Authority dispute 标记（spec §4.2 row 3）+ 集成测试
- M4：Reflect / Consolidate（`mengdie reflect` + proposal/staging）
- v0.2 切片：嵌入向量检索 + Hybrid recall
