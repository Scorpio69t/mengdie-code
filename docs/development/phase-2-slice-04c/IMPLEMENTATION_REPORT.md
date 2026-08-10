# P2-04C 默认交互 TUI 任务闭环实施报告

> 状态：已实现，待 PR 审核  
> 日期：2026-08-10  
> 范围：裸命令默认 TUI、单次任务提交、实时事实、交互审批与安全取消

## 交付

交互终端直接运行 `mengdie` 时，Application Service 默认启动全屏 TUI；`--plain` 保留原有单次文本入口，非 TTY 仍 fail-closed 并提示使用 `mengdie exec`。启动页显示终端 Logo、当前项目、模型、安全等级和中文操作提示，支持多行输入以及 `Ctrl+S` / `Ctrl+Enter` 提交。单个任务按 UTF-8 字节限制为 64 KiB。

TUI 在同一进程通过 Application 拥有的 `TaskRunner` 启动现有单 Agent Runtime。它不构造 Provider、不打开 SQLite、不调用工具，也不持有 Policy 能力。任务开始后只根据 P2-04B 的已提交公开事实更新 `SessionView`；通知丢失时仍通过 `afterSeq` 从 EventStore 补读，内存总线和 Bubble Tea Model 都不是事实源。

## 审批与取消

`ApprovalBroker` 把 Runtime 的阻塞审批请求桥接为一个精确的 TUI 提示。用户可选择允许、拒绝或编辑后重新准备；Broker 只返回用户选择，一次性 Capability 仍由 `policy.Authorizer` 在重新校验后签发。取消上下文或关闭界面会使等待中的审批可靠解阻，失效提示不能再次批准。

运行中按 `Ctrl+C` 或 `q` 只请求取消，不立即把界面伪装成已结束。TUI 进入“正在取消”状态，等待 Agent Runtime 提交 `run.cancelled` 或其他确定终态后才允许退出。程序关闭时 `TaskRunner.Close` 会取消并等待仍活跃的运行，避免后台执行越过界面生命周期。

## 终端与兼容性

- Bubbles v2 的 `textarea` 和 `viewport` 提供维护中的输入、换行、滚动与宽字符基础能力；领域接口不暴露其类型；
- `--no-color` 和 `NO_COLOR` 会移除组件产生的 ANSI 样式，中文与窄屏布局有确定性测试；
- macOS 与 Windows 共同采用 Bubble Tea v2 输入模型，执行与取消继续复用现有 zsh/PowerShell 平台适配；
- `exec`、JSON Lines、`session tui` 历史只读视图和 `--plain` 行为保持兼容。

## 审核后的视觉迭代

首版功能闭环完成后，根据审核意见重新梳理信息层级。参考 [Crush](https://github.com/charmbracelet/crush) 的高密度终端工作台思路，但不复制其品牌和布局细节：宽屏把会话时间线作为主区域，右侧集中展示工作区、会话、模型、安全、进度和待办；窄屏退化为单列，项目、模型与安全信息移入紧凑页头；输入框固定在底部，审批会临时替换输入区，避免与普通消息混淆。

视觉只使用一枚“梦蝶玉”强调色，其余信息依靠字重、边框和间距建立层级，`NO_COLOR` 下仍完整可读。旧的块状灰阶 Logo 被替换为由两组代码尖括号和断开插入光标组成的单色“代码蝶”，并同步 SVG、透明 PNG、终端宽版字符画与紧凑符号。响应式测试额外约束每一行不得超过终端宽度，防止中文、长模型名和路径挤坏布局。

## 明确不做

本切片每个进程只提交一个任务，不实现同一 TUI 内的连续多轮 REPL。跨进程实时广播、daemon、Web、Artifact Store、Patch Journal、成本视图和异步 Swarm 仍不在当前范围。

## 验证

- 单元与集成测试覆盖默认路由、中文/窄屏/无颜色、UTF-8 字节上限、提交、已提交事实通知、审批一次性响应、取消等待和关闭解阻；
- 依赖新增 Bubbles v2.1.1，并按依赖准入规则执行许可证、漏洞与四目标构建检查；
- `go fmt ./...`、`go vet ./...`、`go test ./...`：通过，462 项 / 18 个包；`go test -race ./...` 同样通过；
- `golangci-lint run --build-tags=liveprovider ./...`：0 issue；`govulncheck@v1.1.4 ./...`：未发现漏洞；
- Coding baseline：5/5；live-provider 离线 Harness：3/3；
- `go-licenses` 确认新增 Bubbles/ANSI 为 MIT、clipboard 为 BSD-3-Clause；既有 modernc 许可证定位警告不属于本次新增依赖；
- `CGO_ENABLED=0` 的 Windows amd64、macOS amd64/arm64、Linux amd64 构建通过。
