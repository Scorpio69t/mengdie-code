# P2-04A 只读 Session TUI 实施报告

> 状态：已实现，待 PR 审核  
> 日期：2026-08-10  
> 范围：TUI 依赖准入、公开 SessionView 渲染和非 TTY 降级

## 交付

新增 `mengdie session tui <session-id>`。它通过既有 Application Service 和 Session Service 获取公开 `SessionView`，随后关闭 Store，再启动只读 Bubble Tea 界面；TUI 不持有 SQLite 事务，不访问 Provider，不调用文件或执行工具。

界面展示会话状态、完成消息、工具阶段、未决审批、Todo 与安全说明；支持 `q`/`Ctrl+C` 退出、窗口 resize、窄屏说明、中文文本截断和 `--no-color`。非交互入口明确拒绝，继续使用 `session show --json` 等自动化输出。

## 依赖决策

采用 `charm.land/bubbletea/v2` v2.0.8 与 `charm.land/lipgloss/v2` v2.0.5。两者是 Charm 官方 v2 配套库，MIT、无 CGO；v2 由 Bubble Tea 协调终端 I/O 和颜色降级。Bubbles v2 没有实际调用方，本切片不引入。第三方类型限制在 `internal/tui`，领域模型仍为 `session.SessionView`。

## 安全与恢复边界

- 持久事实仍以 EventStore/Session Reducer 为唯一来源；UI 崩溃不回滚或篡改已提交事件。
- 页面仅显示公开投影，不显示私有任务、工具输出、凭据或 Capability。
- 本切片不实现 EventBus、实时订阅、序号补读、TUI 内命令提交、审批操作或 diff 展开；它们留给 P2-04B。

## 验证

- `go test ./...` 与 `go test -race ./...`：442 项 / 18 个包通过；
- `go vet ./...` 与 `golangci-lint run --build-tags=liveprovider ./...`：通过；
- `govulncheck@v1.1.4 ./...`：未发现漏洞；Coding baseline：5/5；
- `CGO_ENABLED=0` 的 Windows amd64、macOS amd64/arm64、Linux amd64 构建通过。
