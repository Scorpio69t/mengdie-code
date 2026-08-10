# P2-03B2 可恢复审批与执行中只读工具重试实施报告

> 状态：已实现，待 PR 审核  
> 日期：2026-08-10  
> 范围：pending Approval 的重新确认、read/state 中断重试、跨 Run 恢复事实

## 1. 交付结论

`session resume` 现在可以从一个待审批动作，或一个已开始但结果未知的 read/state 工具调用安全继续。恢复不会重放旧授权：每次都会在当前项目状态下重新 `Prepare`，把新的预览交给交互用户确认，再签发仅属于新 Run 的一次性 Capability。

write、execute、network 的执行中断仍保持阻断；Patch Journal 完成前，系统不会猜测它们是否已经产生副作用。多个未完成工具调用也保持阻断，不会替用户选择重试顺序。

## 2. 恢复状态机

| 公开事实 | 恢复动作 | 交互要求 |
|---|---|---|
| `approval.needed` 未决 | `reapprove` | 必须重新 Prepare、展示当前预览、重新审批 |
| `tool.started` 且 effects 仅为 `read`/`state` | `retry_read` | 必须确认重新执行 |
| `tool.started` 含 write/execute/network | 阻断 | 等待 Patch Journal 能证明状态 |
| 多个未完成调用或事实不一致 | 阻断 | 不猜测执行顺序或副作用 |

恢复成功或被拒绝后，`recovery.resolved` 以公开的 Run/Call 身份与结果绑定新旧事实；完整参数、预览正文、输出和凭据不进入公开投影。私有上下文只允许该映射把旧 Assistant 工具调用与新 Run 的工具结果配对。

## 3. 安全与恢复门禁

- Capability 由新的 Run、当前 `PreparedCall` 摘要和新的 nonce 签发；旧审批和旧 Capability 不可跨 Run 使用。
- 无头或 JSON resume 不会尝试交互审批，直接说明拒绝原因；重试只能从终端交互入口发生。
- 重新 Prepare 失败、当前策略拒绝、用户拒绝或用户选择编辑，都不执行工具，并把安全摘要返回模型。
- 恢复工具结果先写入私有上下文，再写入 `recovery.resolved`；无法持久化时不会继续调用 Provider。
- 使用按 `(run_id, call_id)` 的公开 Tool 投影和严格的私有上下文校验，防止同名 Call 在不同 Run 中混淆。

## 4. 自动化验证

- Resume Analyzer 覆盖未决审批、read/state 运行中重试、write/execute/network 阻断，以及多个未完成调用拒绝。
- Agent 测试验证恢复结果先进入 Provider 上下文、重新审批后才执行 read，以及拒绝 write 不产生文件修改。
- Policy 测试验证恢复 Capability 的 nonce 和 RunID 都是新的，旧 Capability 无法供恢复 Run 消费。
- Reducer 测试验证 `recovery.resolved` 仅产生最小的公开恢复事实。

## 5. 未做范围

- 模型请求中断的专用解释与恢复 UI；
- Patch Journal、write 状态判定、回滚和 rewind；
- 完整 TUI、REPL、daemon、Web、异步 Swarm、向量记忆和记忆图谱。
