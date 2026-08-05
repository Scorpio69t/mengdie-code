# 第一阶段 Slice 08 实现报告

## 结果

P1-08 已形成可测试的本地命令执行闭环：`shell` 在批准前固定命令、cwd、timeout、具体 shell、启动 flags 和环境哈希；批准后消费一次性 Capability，重新检查 cwd 与环境，再进入 macOS/Unix process group 或 Windows Job Object。取消和超时会清理受管进程树，命令输出保持有界并可诊断。

协议细节见 [SHELL_PROTOCOL.md](./SHELL_PROTOCOL.md)。

## 交付

- `internal/tools/shell.go`：
  - 严格输入 schema：`command`、可选 `cwd`、可选 `timeout`；
  - 普通命令默认 2 分钟，常见 test/build/check 默认 10 分钟，硬上限 10 分钟；
  - Prepare 选择平台 shell、规范化 cwd、构建环境名称/值哈希绑定并生成完整审批预览；
  - Execute 先消费 Capability，再重查 cwd、shell 文件和完整环境绑定；
  - 非零退出码作为可观察结果返回；timeout/cancel 保留 `errors.Is` 分类并同时返回已捕获结果。
- 环境与隐私：
  - 默认过滤常见 token、secret、password、API key、云平台、GitHub、国内模型、SSH/GPG 及凭据配置入口；
  - 显式敏感环境授权只能由 Application 层注入，不能由模型参数扩大；
  - PreparedCall 与 Preview 只包含环境变量名和值哈希，不包含明文值；
  - Execute 发现变量名称或值变化时，在启动进程前返回 `ErrShellEnvironmentChanged`；
  - 固定非交互变量禁用 Git prompt 与 pager。
- 输出：
  - stdout/stderr 分开流式有界采集，记录原始字节数，保留头尾；
  - 最终输出限制在 64 KiB，非法 UTF-8 替换，ANSI 与其他控制字符可见转义；
  - metadata 提供状态、退出码、耗时、cwd、shell、原始输出大小与强制清理标记。
- `internal/platform`：
  - macOS/Unix：受支持绝对 shell、`-lc`、独立 process group、TERM → 750 ms → KILL、正常结束后的遗留子进程清理；
  - Windows：PATH 中优先 `pwsh.exe`，回退 `powershell.exe`，固定 NoProfile/NonInteractive flags；
  - Windows 使用 `KILL_ON_JOB_CLOSE` Job Object，取消时终止整个 Job；
  - context 在启动前已取消时零进程副作用。
- 默认工具集已注册 `shell`；P1-09 可直接接入 Registry、Policy 与 Approval。

## 依赖决策

新增 `golang.org/x/sys/windows v0.47.0`，只用于 Windows Job Object API：

- 必要性：Go 标准库没有公开完整 Job Object 创建、限制、分配与终止 API；手写 DLL/unsafe 封装会扩大平台安全风险；
- 维护与采用：Go 官方维护的低层系统调用扩展；
- 许可证：BSD-3-Clause；
- 供应链：无新增传递依赖、无 CGO、只进入 Windows 构建；
- 可替换性：所有 x/sys 类型都封装在 `internal/platform/process_windows.go`，Tool 和领域协议只依赖项目自己的 `ProcessSpec/ProcessResult`。

## 验证

- `go fmt ./...`：通过；
- `go vet ./...`：通过；
- `go test ./...`：通过；
- `go test -race ./internal/platform ./internal/tools ./internal/policy`：通过；
- `golangci-lint run ./...`：0 issues；
- `govulncheck@v1.1.4 ./...`：未发现漏洞；
- `mengdie-eval --manifest evals/coding/smoke.json`：5/5 baseline 通过；
- `bd preflight --check`：测试、lint、格式与 Beads 污染检查通过；`go.sum` 因本切片新增已审核依赖而变化，但仓库没有 Nix 文件需要同步；保留既有 AGENTS.md/CLAUDE.md 差异警告；
- Windows 本机真实运行：PowerShell 正常/非零退出、超时、无 stdin、大输出、环境过滤、Job Object 子孙进程取消全部通过；
- `GOOS=linux` 与 `GOOS=darwin` 对 `internal/platform`、`internal/tools` 测试二进制交叉编译通过；真实 Unix 进程组测试将随本切片三平台 PR 执行；
- 堆叠基线 PR #14 已通过 macOS、Windows、Ubuntu 和质量检查。

测试覆盖：Capability 缺失零副作用、cwd 逃逸和批准后 symlink 替换、环境值变化、敏感环境默认过滤与显式授权、命令控制字符、非法 timeout/字段、stdout/stderr/Unicode/非零退出、64 KiB 截断、ANSI 转义、无 stdin、timeout，以及父进程取消后子进程不存活。

## Deep Agent 门禁落实

- 上下文：审批只保留完整命令和环境名称/哈希；输出限为 64 KiB，原始字节数作为证据；Artifact Store 留待 M2；
- 安全：Policy/Approval/Capability 位于进程启动之前；shell 不能冒用文件工具权限，UI 明示“受控本地执行，不是强沙箱”；
- 中断：context 贯穿 Tool 与平台 runner，进程树是取消单元；timeout、cancel、非零退出和启动失败不混为同一状态；
- 恢复：M1 不恢复中断命令，也不自动重放 execute；P1-09 必须把未知/中断结果返回模型，M2 再通过事件持久化恢复；
- 评测：正常、拒绝、越界、秘密、输出、取消和平台差异都有自动化证据，不因功能完成提前引入 daemon、PTY 或沙箱框架。

## 明确不做

- Agent Runtime、CLI allowlist 和 `--allow-env` 接线（P1-09）；
- shell 命令 AST、网络行为推断或通用安全解析器；
- PTY、交互 stdin、后台任务；
- OS 强沙箱、容器和网络隔离；
- EventStore、Artifact Store、resume 与跨进程重放（M2）；
- daemon、Web、异步 Swarm、向量记忆和记忆图谱。
