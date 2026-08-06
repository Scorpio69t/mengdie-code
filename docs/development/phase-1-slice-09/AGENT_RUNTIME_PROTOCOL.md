# Agent Runtime 与 todo 协议

本文记录第一阶段 Slice 09 的最小 Agent 执行契约。目标是把已完成的 Provider、Tool、Policy、Approval 和事件边界组成真实但有界的 `model → tool → observe` 闭环，同时保持 M1 单进程、单 Agent、内存状态的范围。

## 运行边界

- 一个 `exec` 进程只运行一个 Agent 和一个 RunState；所有工具串行执行；
- RunState 只包含当前 run 的消息、todo、turn、usage 和拒绝计数，进程退出即丢失；
- Event 仍为 M1 内存流，不实现 EventStore、resume 或跨进程重放；
- Provider 返回的最终文本和工具结果进入上下文，隐藏推理不保存、不显示；
- Runtime 不获得绕过 Policy 的执行入口。

## 上下文契约

每轮请求按以下顺序构建：

1. 核心安全与工具规则；
2. 用户级 AGENTS.md；
3. 仓库根到 cwd 的 AGENTS.md 链，越近的指令越后出现；
4. 当前 todo；
5. 当前 run 的原始消息和真实工具结果。

AGENTS.md 单文件最多 64 KiB、总量最多 256 KiB，只提供行为指导，不能授予额外工具权限。M1 使用保守 token 估算；超过模型窗口时返回 `context_budget`，不静默删除任务、todo、审批拒绝或最后验证结果。自动摘要和 Artifact Store 属于 M2。

Provider 工具 schema 按名称稳定排序。只有能力明确支持时才发送 strict schema、parallel tool 或 stream usage 可选字段；Runtime 即使收到多个 tool call 也保持串行执行。

## 工具执行链

每个外部工具调用遵循：

```text
Provider ToolCall
  → Registry Lookup
  → Prepare（无副作用）
  → tool.proposed
  → Policy / Approval
  → 一次性 Capability
  → tool.started
  → Execute
  → tool.completed
  → 结构化 Tool Message 返回模型
```

未知工具、非法参数、Policy 拒绝、审批编辑、启动失败、timeout 和 cancel 都以结构化失败结果返回模型。模型可以选择安全替代方案，但不能把失败声称为成功。无头模式发生 Policy 拒绝后，即使模型随后给出解释，CLI 仍返回退出码 4。

## `write_todos`

`write_todos` 使用独立 `state` effect：它只替换当前 RunState 的计划，不获得文件、进程或网络权限。它仍走 Prepare、digest 和一次性 Capability，以保持统一审计链。

Todo 字段：

```json
{
  "id": "run-tests",
  "content": "运行目标测试",
  "status": "in_progress"
}
```

状态只能是 `pending`、`in_progress`、`completed`、`cancelled`；同一计划最多 64 项、ID 唯一、最多一个 `in_progress`。更新成功先产生 `todo.updated`，再成为后续上下文的一部分。Todo 不写入仓库文件，也不是 M2 的持久任务。

## 无头授权

- 普通项目读取与 `state` effect 默认允许；
- edit/write 默认拒绝，`--allow-edit` 只对 `edit_file` 与 `write_file` 生成当前 run 的 allow rule；
- shell 只接受 CLI 或 profile 的命令前缀 allowlist；`--allow-command go,test` 表示 `go test`；
- 命令前缀按 token 边界比较，含换行、`;`、`&`、`|`、重定向、反引号或 `$(` 的命令不能命中自动授权；
- `--allow-env NAME` 只能由用户 CLI 注入，模型参数不能扩大环境继承；
- 网络工具、项目外路径和受保护路径写入继续硬拒绝。

命令 allowlist 不是 shell 沙箱。即使被允许的命令也在用户账户下运行；产品继续显示“受控本地执行”。

## 循环保护与终态

- 默认最多 32 turns，范围 1–256；
- 连续三次相同工具名与规范化参数，在第三次执行前停止；
- 连续三次相同失败结果停止；任一成功工具结果会重置失败计数；
- 上下文超限、Provider 协议错误、最大回合和重复循环产生 `run.failed`；
- 用户取消与 timeout 产生 `run.cancelled`，不会伪造成普通失败或完成；
- Event Sink 写失败立即终止 Runtime，不继续产生未被观察的副作用。

## 当前不做

- 持久事件、session resume、崩溃恢复与幂等 Command；
- 自动上下文压缩、Artifact Store 与 Patch Journal；
- 多 Agent、并行工具、后台任务、daemon 与 Web；
- 交互式 PTY/stdin、强沙箱和网络隔离；
- 真实 Provider 兼容矩阵与实机 smoke（P1-10）。
