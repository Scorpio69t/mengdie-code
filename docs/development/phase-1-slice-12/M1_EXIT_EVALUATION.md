# P1-12：双平台真实 Provider Coding 预验收

## 1. 用户痛点与所属层次

单元测试、fake Provider 和交叉编译只能证明组件按预期工作，不能证明真实模型能在 macOS 与 Windows 上稳定完成 Coding 闭环。本切片属于 Harness 评测层：建立可重复、可审计的真实 Provider 预验收入口，不增加 Agent 自治能力，也不把仓库内 fixture 冒充外部真实仓库使用记录。

本轮最小闭环是：

```text
隔离复制任务 → 真实 Provider → read_file → edit/write → go test
           → 独立 verifier 再验证 → 输出工作流证据
```

## 2. 触发方式

GitHub Actions 的 `Provider 实机 Smoke` 仅支持 `workflow_dispatch`。选择：

- `provider`：`deepseek` 或 `kimi`；
- `suite`：`readonly` 或 `m1-coding`。

`m1-coding` 在 `macos-latest` 和 `windows-latest` 分别执行 `evals/coding/smoke.json` 的 5 个任务。工作流绑定 `provider-smoke` Environment；建议配置 required reviewers，并在 Environment 中保存对应 Provider Secret。

该套件包含付费网络请求，不在普通 push/PR CI 中自动执行，也不允许同一 Provider 与套件并发运行。

## 3. 上下文、权限与事实边界

每个任务使用独立临时目录，只复制公开 fixture。注入模型的上下文来自任务 prompt、当前临时仓库、工具 schema 和现有 M1 系统规则；测试不会把其他任务输出或历史对话带入下一项。

权限固定为：

- 允许项目内只读工具；
- 允许本次 run 的 edit/write；
- 只允许 `go test` 命令前缀；
- manifest 为每项任务声明唯一允许变化的实现文件；测试、依赖文件和未声明新文件保持哈希不变；
- 不允许其他 Shell、项目外文件、网络工具或交互审批；
- Provider Key 只供宿主 Provider Client 使用，不传给独立 verifier，Shell 仍按产品环境过滤规则执行。

M1 仍然没有 EventStore、Artifact Store 或 session resume。工作流日志与 GitHub run URL 是本切片的外部验收证据，不冒充产品内持久会话。

## 4. 判定规则

每个任务必须同时满足：

1. CLI 以成功退出码结束，且没有 Policy 拒绝；
2. JSON Lines 中存在成功的 `read_file`、`edit_file`/`write_file`、`shell` 和 `run.completed`；
3. 不出现 `approval.needed`；
4. stdout/stderr 不包含 Provider Key；
5. 前后文件哈希证明所有 diff 都在 `acceptance.allowed_changes` 白名单内，且至少一个白名单文件实际变化；
6. Agent 结束后，独立 argv verifier 在同一临时工作区再次运行 manifest 命令并成功。

模型声称“测试通过”不是证据；一次 shell 成功也不能替代独立 verifier；修改测试来伪造通过会被哈希门禁拒绝。任一任务失败都会让对应平台 Job 失败，不把未知或部分成功写成通过。

## 5. 恢复、清理与限制

- 任务失败后保留 GitHub 日志，但临时工作区由 Go 测试框架清理；不会写回源码仓库。
- Job 超时 60 分钟，Go 测试总超时 50 分钟；每个 manifest verifier 使用自己的有界超时。
- 工作流不自动重试付费任务。需要重跑时由维护者检查失败类别后手动触发。
- 当前 5 个任务都是仓库内小型 Go fixture；它们只证明有界的真实 Provider 读改测闭环，不代表外部真实仓库任务、Coding Daily Set、Long-run Set 或 Memory Trust Set 已完成。
- 完整 TUI、EventStore、resume、rewind 和记忆仍属于后续里程碑。

## 6. M1 计入规则

只有同一审核提交上的 macOS 与 Windows `m1-coding` Job 均成功，才能记录“各平台 5 个真实 Provider fixture 任务”。PR 编译通过、只读 smoke 通过或验收入口合并都不能替代这次真实运行。这项记录仍不能替代第一阶段详细设计要求的外部真实仓库任务记录。

运行后应在 P1-12 Beads 记录：commit、Provider、两个 Job URL、任务通过数、未授权副作用数和是否发现密钥泄漏。M1 仍需同时满足第一阶段详细设计中的外部真实仓库任务、连续 20 次 main CI、安全专项与其余出口条件。
