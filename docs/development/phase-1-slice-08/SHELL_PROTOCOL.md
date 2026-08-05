# Shell 与进程协议

本文记录第一阶段 Slice 08 的稳定执行边界。目标是在 macOS 与 Windows 上，让用户看到并批准一个完整、确定的本地命令契约，并保证取消或超时时清理受管进程树。

## 安全定位

`shell` 声明 `execute` Effect，必须经过 P1-06 的 Policy、Approval 和一次性 Capability。它提供的是**受控本地执行**，不是 OS 强沙箱：命令在用户账户下运行，获得批准后仍能访问该账户本来可以访问的文件和网络。

文件工具的 PathGuard 不能约束 shell 命令正文。审批界面必须完整展示命令，不得把“cwd 位于项目内”描述为“命令只能访问项目内”。M1 不宣传网络隔离、容器隔离或凭据完全隔离。

## 输入与批准契约

模型输入：

```json
{
  "command": "go test ./internal/parser",
  "cwd": ".",
  "timeout": "10m"
}
```

- `command` 必填，最多 16 KiB，只允许无 NUL、无 ANSI/危险控制字符的 UTF-8 文本；命令内容不做跨 shell 的伪解析或重写；
- `cwd` 默认项目根，必须由 PathGuard 解析为项目内目录；Prepare 与 Execute 各解析一次；
- `timeout` 使用 Go duration，范围 1 秒至 10 分钟；普通命令默认 2 分钟，常见 test/build/check 命令默认 10 分钟；
- stdin 永远不可用；Windows 额外使用 PowerShell `-NonInteractive`，等待输入的命令只能收到 EOF/非交互错误或超时；
- 未知字段直接拒绝。

Prepare 无外部副作用。它把以下内容规范化后写入 PreparedCall，并由 digest 与 Capability 绑定：

- 原始完整命令；
- 规范化绝对 cwd；
- 毫秒级超时；
- 选定 shell 的绝对路径和固定启动参数；
- 继承环境变量的名称与值哈希；
- 用户配置显式允许的敏感环境变量名称。

审批预览显示安全等级、shell 路径及 flags、cwd、超时、继承环境变量名和完整命令。环境变量值及其明文不会进入 PreparedCall、digest 输入之外的结构、预览或日志；digest 中只包含值哈希。

## 环境边界

默认继承当前进程环境，但过滤名称中常见的 token、secret、password、API key、access key、credential、cookie、session，以及 AWS、Azure、Google、GitHub、OpenAI、Anthropic、DeepSeek、Kimi、智谱、SSH、GPG 等凭据相关变量。`KUBECONFIG`、`DOCKER_CONFIG`、`NETRC` 等凭据入口也默认过滤。

显式允许由 Application/CLI 配置通过 `AllowedEnvironment` 注入，模型参数不能自行扩大。审批只显示被允许的变量名。Execute 重新构建环境并比较全部名称和值哈希；任何变化都会返回 `ErrShellEnvironmentChanged`，命令不会启动。

固定加入以下非秘密、非交互约束：`CI=1`、`GIT_TERMINAL_PROMPT=0`、`GIT_PAGER=cat`、`PAGER=cat`、`NO_COLOR=1`、`TERM=dumb`。环境最多 256 项，显式敏感授权最多 32 项。

环境过滤不能阻止命令从用户主目录、shell 初始化文件、凭据代理或操作系统凭据库读取数据。尤其 macOS 的 `zsh -lc` 会按 shell 自身规则加载登录环境；这是本地执行的已知边界，不应被 UI 隐藏。

## 平台 Shell

### macOS / Unix

- `$SHELL` 只有在它是绝对路径、普通可执行文件且 basename 为 `zsh`、`bash` 或 `sh` 时才接受；
- macOS 依次回退 `/bin/zsh`、`/bin/bash`、`/bin/sh`；Linux 依次回退 `/bin/bash`、`/bin/sh`；
- 固定以 `-lc <command>` 执行；
- 进程加入独立 process group。

### Windows

- 在批准环境的绝对 PATH 项中优先寻找 `pwsh.exe`，再寻找 `powershell.exe`；
- 固定参数为 `-NoLogo -NoProfile -NonInteractive -Command <command>`；
- 不经过 `cmd.exe /c` 二次拼接；
- 启动窗口隐藏，进程加入带 `KILL_ON_JOB_CLOSE` 的 Job Object；
- Job Object 封装使用 Go 官方维护的 `golang.org/x/sys/windows`，第三方类型不离开 `internal/platform`。

## 取消、超时与进程树

- Unix 取消时向整个 process group 发送 TERM，等待 750 ms 后对仍存活的组发送 KILL；正常命令结束后也会检查并清理遗留后台子进程；
- Windows 取消时终止整个 Job Object；正常返回时关闭 Job handle，`KILL_ON_JOB_CLOSE` 清理仍存活后代；
- `exec.Cmd.WaitDelay` 限制后代进程持有 stdout/stderr pipe 时的等待；
- context 已取消时不得启动进程；
- timeout 保留 `context.DeadlineExceeded`，用户取消保留 `context.Canceled`，调用方可用 `errors.Is` 稳定分类；
- 取消/超时仍返回已捕获的有界输出、状态和清理证据。

## 输出契约

stdout 与 stderr 分开流式采集，每路只保留有界头尾并记录原始字节数；最终合并结果不超过统一的 64 KiB 工具输出预算。非法 UTF-8 字节替换为 `�`，不把控制序列交给终端 renderer。

ToolResult metadata 至少包含：

- `status`：`completed` / `timeout` / `cancelled`；
- `exit_code`；
- `duration_ms`；
- `cwd` 与 `shell`；
- `stdout_bytes` / `stderr_bytes`；
- `forced_cleanup`。

非零退出码是可供 Agent 观察和修复的正常命令结果，不作为工具协议错误。启动失败、控制失败、timeout 和 cancel 返回可分类错误；后两者同时保留 ToolResult。

## 当前不做

- Agent Runtime、CLI `--allow-command` / `--allow-env` 接线（P1-09）；
- 命令 AST、跨 shell 通用解析器或自动重写；
- 交互式 PTY/stdin；
- OS 强沙箱、容器、网络隔离；
- 后台任务、detach/reattach、daemon；
- EventStore、Artifact Store 和跨进程恢复（M2）。
