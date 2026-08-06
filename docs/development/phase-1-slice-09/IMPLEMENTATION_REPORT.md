# 第一阶段 Slice 09 实现报告

## 结果

P1-09 已把离线 Provider、工具、Policy/Approval 与事件协议组合为最小单 Agent 闭环。fake Provider 可以在真实临时 Go 仓库中完成 read、exact-edit、shell test、todo 更新与最终回答；拒绝结果进入下一轮模型上下文且保持零副作用。

协议细节见 [AGENT_RUNTIME_PROTOCOL.md](./AGENT_RUNTIME_PROTOCOL.md)。

## 交付

- `internal/agent`：内存 RunState、串行 model/tool loop、流式事件适配、usage、稳定错误分类、最大回合与双重重复保护；
- `internal/context`：安全提示、AGENTS.md、todo、消息与工具 schema 的有界构建；不支持能力字段时不发送可选 Provider 参数；
- `internal/tools/write_todos.go`：run-scoped `state` effect、严格 schema、唯一 ID 与单一 `in_progress`；
- `internal/project/agents.go`：用户级和项目路径链加载，单文件与总量限制、UTF-8 和越界检查；
- `internal/policy`：state 自动允许、shell token 前缀规则、控制操作符硬化、本地审批展示完整 bounded preview；
- `internal/app`：`mengdie exec` Runtime 接线、Provider 构建、退出码、`--allow-edit`、`--allow-command` 与 `--allow-env`；
- `internal/ui/terminal`：流式 delta 与 completed 事件不重复打印最终文本。

## Deep Agent 门禁

- 上下文：任务、todo、拒绝和真实验证结果不被静默裁剪；超预算明确失败；AGENTS.md 不进入事件日志；
- 安全：Runtime 没有直接执行函数，所有副作用继续通过 Policy、Approval 与一次性 Capability；state effect 不提供外部权限；
- 中断：cancel、timeout、Provider 错误、重复循环和最大回合均有唯一终态事件；
- 恢复：M1 明确不恢复中断 run，也不自动重放工具；EventStore 与幂等 Command 留待 M2；
- 评测：fake Provider 使用真实 read/edit/shell 工具完成临时仓库闭环，同时覆盖拒绝、重复、预算和跨平台协议。

## 明确不做

- 真实 DeepSeek/Kimi smoke 与能力矩阵（P1-10）；
- 完整交互会话、Provider doctor 增强与发布预览；
- EventStore、resume、压缩、Artifact、Patch Journal 与可信记忆；
- daemon、Web、MCP、子 Agent、PTY 和强沙箱。

## 验证

- `go fmt ./...`：通过；
- `go vet ./...`：通过；
- `go test ./...`：16 个包、328 个测试通过；
- Runtime 相关包 `go test -race`：6 个包、194 个测试通过；
- `golangci-lint`：零问题；
- `govulncheck`：未发现已知漏洞；
- Coding baseline：5/5 通过；
- `bd preflight --check`：功能检查通过；保留仓库既有的 AGENTS.md/CLAUDE.md 差异警告与不适用的 Beads 源码版本检查跳过项。

macOS、Windows、Ubuntu 由 PR CI 继续验证。
