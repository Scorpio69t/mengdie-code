# 第一阶段设计笔记

## 现状检查

- `ARCHITECTURE.md` 已把 Windows 写成单独差异化，容易让 macOS 用户误以为不是重点平台。
- `README.md`、`README_EN.md` 和 `CONTRIBUTING.md` 均重复了 Windows 单平台表述，需要同步修改。
- 当前 CI 只运行 Ubuntu，和“一等平台”承诺不一致。
- M1 在架构路线图中的交付是 Provider、Agent 循环、基础工具、Policy/Approval、极简 CLI/TUI、中断与错误处理。
- M2 才负责 EventStore、resume、Artifact Store、上下文压缩和 Patch Journal；M1 设计不能越界实现这些能力。

## 平台策略

### Tier 1

- macOS：Apple Silicon 为主要目标，Intel 尽力兼容；重点验证 Terminal.app、iTerm2、zsh、Keychain 友好配置和 Homebrew 分发路径。
- Windows：Windows 10/11 x64；重点验证 Windows Terminal、PowerShell 7、路径/盘符/UNC、Job Object 进程树终止和 DPAPI 友好配置路径。

### Tier 2

- Linux：保持构建、测试和完整 CLI 支持；OS 级沙箱可以先在 Linux 探索，但不应反向定义全部产品体验。

## M1 边界

### 必须有

- OpenAI-compatible 流式 Provider；
- 单进程 Agent loop；
- read/search/edit/write/shell/write_todos 工具；
- Policy + Approval；
- macOS/Windows 进程、路径和终端适配；
- `mengdie`、`exec`、`doctor`、`--version`；
- fake provider 与真实 HTTP 契约测试；
- 真实仓库 smoke task。

### 明确没有

- daemon、Web、MCP、子 Agent；
- EventStore、resume、持久会话；
- 可信记忆与 reflect；
- Patch Journal；
- 完整 Bubble Tea TUI；
- OS 级强沙箱承诺。

## 设计偏好

- 配置用 Go 标准库加轻量 TOML 解析；密钥只引用环境变量，不写入项目配置。
- OpenAI-compatible 首版基于 `/chat/completions` + SSE，因为国内兼容端点覆盖更广。
- 修改工具优先采用“唯一旧文本 → 新文本”的确定性编辑，而不是首版自行实现完整 unified diff 应用器。
- shell 默认每次审批；无头模式默认禁用 shell，除非显式 allowlist。
- M1 事件只在进程内流转，但事件形状应能平滑进入 M2 EventStore。

## 本轮落实

- 中文与英文首页已改为 macOS / Windows 双一等平台定位。
- 总体架构与贡献指南已同步双平台策略。
- CI 已拆为 Ubuntu 质量检查与 macOS/Windows/Linux 测试矩阵。
- M1 详细设计已覆盖 22 个章节、12 个工作包、四周纵向切片和出口验收。
- 详细设计明确不提前实现记忆、复盘、daemon、Web、resume 和完整 TUI。

