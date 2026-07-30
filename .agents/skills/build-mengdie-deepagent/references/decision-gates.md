# MengDie Deep Agent 决策门禁

## 目录

1. 切片入口
2. 上下文门禁
3. 规划与状态门禁
4. 工具与 Provider 门禁
5. 委派与异步门禁
6. 记忆与 Skill 门禁
7. 安全与恢复门禁
8. 可观测性与评测门禁
9. 里程碑应用
10. 决策记录模板

## 1. 切片入口

开始实现前必须回答：

- 用户的具体痛点是什么？
- 哪个指标或验收场景会改善？
- 本轮最小端到端闭环是什么？
- 它属于 Runtime、Framework、Harness 还是 UI？
- 哪些能力明确留待后续？
- 如果不做这一层抽象，当前闭环是否仍能完成？

不能回答时，只做调研或设计，不创建生产接口。

## 2. 上下文门禁

为每类信息填写去向：

| 信息 | 常驻 | 按需读取 | Artifact | 短期状态 | 长期记忆 |
|---|---:|---:|---:|---:|---:|
| 安全和工具规则 | 是 |  |  |  |  |
| 当前任务与 todo | 是 |  |  | 是 |  |
| 大型日志与 diff |  | 摘要 | 是 |  |  |
| 测试退出码与失败项 | 是 |  | 是 | 是 |  |
| 历史项目事实 |  | 是 |  |  | 候选 |

检查：

- 为输入、工具输出和总上下文设置预算。
- 大输出保留路径、哈希、关键片段和生命周期。
- 压缩前后保留任务、用户纠正、审批和未解决错误。
- 原始事实可以从 EventStore 或 Artifact Store 找回。
- 摘要不覆盖原始证据，不提升记忆权威。

## 3. 规划与状态门禁

- 简单任务允许直接执行；复杂任务先建立 todo。
- 最多一个 todo 为 `in_progress`。
- 计划变化必须说明触发它的新证据。
- todo、run、tool call 和 approval 都有稳定 ID。
- Command 重试不重复产生副作用。
- 崩溃后根据最后持久事件恢复，不根据 UI 文案猜测。
- 模型调用中断标记为未知或中断，不伪造续流成功。

## 4. 工具与 Provider 门禁

每个工具必须声明：

- 名称、用途、输入 schema、输出 schema；
- read/write/execute/network/external effect；
- 可访问资源和工作目录；
- 超时、输出上限、取消与进程树处理；
- 幂等性、重试策略和审计字段；
- macOS 与 Windows 的差异。

每个 Provider 必须验证：

- 工具调用与流式工具参数；
- 严格 schema、并行工具和推理内容能力；
- usage、缓存、限流、重试与超时；
- 原始错误与 Provider 事件是否可诊断；
- capability probe 失败时的明确降级。

模型声称安全或兼容不能替代上述契约。

## 5. 委派与异步门禁

引入子 Agent 前必须同时满足：

- 单 Agent 基线存在可复现的上下文或时延瓶颈；
- 子任务能独立描述并限制输入；
- 子 Agent 只需最小工具和权限；
- 返回结果可结构化、可限制大小、可验证；
- 失败不会让主任务状态含糊；
- 评测证明收益超过额外成本和失败率。

引入异步前还必须具备：

- 持久 task/thread/run ID；
- 最新状态查询，而非引用旧消息；
- update、cancel、resume 语义；
- Worker/队列/配额/超时/清理；
- 跨主任务与子任务的 trace 关联；
- 用户可理解的后台任务状态。

任一项缺失时保持单 Agent 或同步执行。

## 6. 记忆与 Skill 门禁

### 记忆

- 明确 thread、user、project、branch、task 作用域。
- 保存来源、观察时间、有效期、权威、状态和冲突。
- 用户规则、仓库事实、命令验证和模型推断分级。
- 召回只记录使用，不自动加强证据。
- 当前仓库可验证事实优先实时读取。
- 共享或组织记忆默认只读；自动整合只写候选区。
- 同文件并发写入有冲突方案或通过拆分避免。

### Skill

- description 能准确触发且不与其他 Skill 重叠。
- SKILL.md 聚焦流程，详细内容放 references。
- 用户级与项目级来源有确定优先级。
- Skill 内容可读不代表可执行；脚本仍经过 Tool Policy。
- 正式 Skill 的自动修改必须先形成提案并审批。

## 7. 安全与恢复门禁

按 effect 制定默认策略：

| Effect | 默认策略 |
|---|---|
| read | 项目根内允许，敏感路径拒绝 |
| write | 展示 diff，窄范围批准，记录 Journal |
| execute | 命令与参数策略、超时、环境过滤 |
| network | 独立授权，展示目标和凭据边界 |
| destructive/external | 高风险确认；宽泛目标直接拒绝 |

必须验证：

- Policy 位于模型和真实执行之间。
- Approval 绑定规范化参数、资源、工作目录、有效期和 nonce。
- 路径规范化后再判定，覆盖 `..`、绝对路径、symlink、Windows reparse point、UNC 和盘符。
- 文件权限不被 Shell、MCP 或自定义工具旁路。
- 沙箱凭据尽量留在宿主侧，下载产物默认不可信。
- 目录删除全有或全无。
- 写入前后哈希与逆向 patch 支持安全 rewind。
- 用户后续修改导致哈希不匹配时停止回滚。

## 8. 可观测性与评测门禁

至少产生并验证：

- run started/completed/failed/interrupted；
- model request/stream/usage/error；
- tool proposed/approved/started/completed；
- todo updated；
- context evicted/compacted；
- memory recalled/proposed/changed；
- cost updated。

评测至少覆盖：

- 正常闭环；
- 模型或 Provider 错误；
- 工具拒绝与参数编辑；
- 路径越界和旁路尝试；
- Ctrl+C、崩溃、resume 与幂等重试；
- 大输出卸载与压缩后的任务保持；
- macOS、Windows 及 Linux CI；
- 无关 diff、秘密泄漏和未授权副作用为零。

## 9. 里程碑应用

- **M0：** 先固定 Coding、Long-run、Memory Trust 的可复跑入口；不实现高级机制。
- **M1：** 单 Agent、Provider、工具、todo、Policy、Approval、事件输出形成真实 Coding 闭环。
- **M2：** EventStore、Artifact、resume、rewind、AGENTS.md、Skill 和成本建立可恢复 Harness。
- **M3：** 在证据模型、作用域、冲突和审计齐备后实现长期记忆。
- **M4：** Reflect 只产生可审核提案，用接受率和后续复用率证明价值。
- **M5：** daemon、Web、MCP、子 Agent、强沙箱、向量或图记忆均由基线数据触发。

不得为了展示架构完整度跨越里程碑。

## 10. 决策记录模板

```markdown
## 决策：<标题>

- 用户痛点：
- 所属层次：Runtime / Framework / Harness / UI
- 当前基线：
- 选择方案：
- 上下文与持久化边界：
- 工具与风险：
- 中断、恢复、回滚：
- 跨平台差异：
- 评测与验收：
- 明确不做：
- 升级条件：
```
