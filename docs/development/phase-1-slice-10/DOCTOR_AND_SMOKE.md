# P1-10：Doctor 与 Provider 实机 Smoke

## 1. 目标与边界

本切片把“配置看起来没问题”推进为可重复诊断的最小闭环：Doctor 同时检查本机 Harness、项目边界和 Provider 工具调用能力；真实 Provider smoke 通过受保护的手动工作流验证 `exec → read_file → 完成`。它不引入 daemon、Web、持久化会话、强沙箱或自动修改配置。

设计门禁：

- 本地诊断必须确定、快速，并可用 `--offline` 保证零 Provider 网络访问；
- 在线探测只发送固定诊断内容，不发送用户任务、源码、路径或环境变量值；
- 输出只包含脱敏事实和稳定错误分类，不记录 Provider 原始响应；
- 真实 smoke 必须显式启用、只读、有限轮次，不能进入普通 PR CI；
- macOS 与 Windows 是同等验证目标。

## 2. Doctor 使用方式

```bash
# 只检查本地配置、项目、工具和终端；不会构造 Provider
mengdie doctor --offline

# 执行本地检查，并进行一次有限在线工具调用探测
mengdie doctor

# 机器可读输出
mengdie doctor --offline --json
mengdie doctor --json --probe-timeout 20s
```

`--probe-timeout` 默认 15 秒，只接受 1 秒到 1 分钟。Doctor 检查：

- 版本、Go 版本、操作系统和架构；
- 用户/项目配置、当前 profile、Provider、模型和密钥环境变量是否存在；
- 项目根、Git 可执行文件及已跟踪文件状态；
- 从用户级到当前目录的 `AGENTS.md` 生效链；
- zsh 或 PowerShell、rg 及 Go walker fallback；
- TTY、颜色和交互输入能力；
- 当前真实安全标签：`受控本地执行（非强沙箱）`；
- Provider 声明能力与固定 `doctor_echo` 工具调用闭环。

Doctor 调用 Git 时只继承 PATH、HOME、系统目录、区域设置和临时目录等必要环境，不继承 API Key、Token 或其他通用变量。JSON 中的项目路径和用户配置路径使用 `$PROJECT_ROOT`、`$USER_CONFIG` 和 `~` 表示；Provider 地址只保留 scheme 与 host；密钥只报告“已设置/未设置”。

## 3. 在线探测协议

在线模式使用固定的系统消息、固定用户消息和无副作用工具：

```text
tool: doctor_echo
arguments: {"value":"MENGDIE_DOCTOR_OK"}
tool_choice: required
```

只有收到名称和参数都完全匹配的工具调用才通过。探测不提供文件、Shell、编辑或网络工具，不包含项目上下文，也不会执行模型返回的任意动作。

`provider_probe` 只记录：是否尝试、状态、稳定错误类别/代码、耗时和是否验证工具调用。认证失败、限流、超时、服务端错误和协议错误沿用 Provider 错误分类；原始响应正文不会写入报告。

## 4. 状态与退出码

单项检查状态为 `pass`、`warn`、`fail` 或 `skip`。总状态为：

- `ok`：没有 warning 或 failure；
- `warning`：至少一个 warning，但没有 failure；
- `error`：至少一个 failure。

进程退出码：

| 退出码 | 含义 |
| --- | --- |
| 0 | 检查通过，或只有 warning/skip |
| 1 | 本地检查失败或输出失败 |
| 2 | 参数或配置输入无效 |
| 3 | Provider 在线探测失败 |

JSON schema 当前版本为 `1`。新增字段应保持向后兼容；改变既有字段含义或删除字段必须提升 schema 版本。

## 5. 国内 Provider 配置

仓库提供三个无密钥样例：

- [`configs/examples/deepseek.toml`](../../../configs/examples/deepseek.toml)
- [`configs/examples/kimi.toml`](../../../configs/examples/kimi.toml)
- [`configs/examples/config.toml`](../../../configs/examples/config.toml)

复制所需样例到平台用户配置目录，或者项目内 `.mengdie/config.toml`，再设置密钥：

```zsh
# macOS / zsh
export DEEPSEEK_API_KEY='...'
# 或 export MOONSHOT_API_KEY='...'
```

```powershell
# Windows / PowerShell；仅对当前进程生效
$env:DEEPSEEK_API_KEY = '...'
# 或 $env:MOONSHOT_API_KEY = '...'
```

然后依次运行：

```bash
mengdie doctor --offline
mengdie doctor
mengdie exec --json "检查当前项目"
```

样例核验日期为 2026-08-06。当时 DeepSeek 官方当前模型为 `deepseek-v4-flash` / `deepseek-v4-pro`，Kimi 当前通用模型为 `kimi-k3`，Kimi 另有 Coding 模型 `kimi-k2.7-code`。Provider 会调整模型生命周期和能力，升级前必须重新核验官方文档：

- [DeepSeek 模型与价格](https://api-docs.deepseek.com/quick_start/pricing)
- [DeepSeek Tool Calls](https://api-docs.deepseek.com/guides/tool_calls)
- [Kimi 模型列表](https://platform.kimi.ai/docs/models)
- [Kimi K3 快速开始](https://platform.kimi.ai/docs/guide/kimi-k3-quickstart)

## 6. 真实 Provider smoke

`.github/workflows/provider-smoke.yml` 只允许 `workflow_dispatch` 手动触发，并绑定 GitHub Environment `provider-smoke`。仓库维护者需在该 Environment 配置：

- `DEEPSEEK_API_KEY`；
- `MOONSHOT_API_KEY`；
- 建议启用 required reviewers，避免误触发付费调用。

触发时选择 DeepSeek 或 Kimi；工作流在 `macos-latest` 与 `windows-latest` 各执行一次。测试还要求 Go build tag `liveprovider` 和 `MENGDIE_LIVE_SMOKE=1`，形成双重 opt-in。

真实 smoke 创建临时项目和固定标记文件，要求 Agent 必须调用 `read_file` 后完成任务。它不授予 edit/write/shell，限制为 8 轮，并验证：

- 运行完成且确实提出 `read_file`；
- 固定文件未被修改；
- stdout/stderr 不包含 Provider 密钥。

普通 `push` 和 `pull_request` 不会执行外部或付费 Provider 请求。没有真实密钥时，本地只编译并跳过该测试：

```bash
go test -tags=liveprovider ./internal/app -run '^TestLiveProviderCompletesReadOnlyToolTask$' -count=1 -v
```

## 7. 已知限制

- 默认在线 Doctor 会产生极少量 Provider token 和一次网络请求；需要绝对离线时必须使用 `--offline`。
- `Capabilities` 中未被适配器确认的可选能力保持 false；Doctor 不根据营销文档猜测能力。
- 真实 smoke 验证最小只读闭环，不代表复杂 Coding 任务、长上下文、编辑或 Shell 已完成实机验收。
- 当前执行层是受控本地执行，不是操作系统级强沙箱。
