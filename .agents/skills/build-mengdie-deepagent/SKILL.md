---
name: build-mengdie-deepagent
description: Guide MengDie Code architecture, planning, implementation, review, and evaluation with Deep Agent harness principles. Use for every MengDie milestone or feature involving the Agent Runtime, context management, tools, planning, Skills, memory, delegation, Human-in-the-Loop, sandboxing, permissions, observability, or safety-sensitive CLI behavior.
---

# Build MengDie Deep Agent

将 Deep Agent 的 Harness 思想转化为 MengDie Code 可执行、可验证的 Go/CLI 工程约束。吸收原则，不照搬 Python、LangChain 或某个 SDK 的 API。

## 必读资料

开始任何设计、编码或审查前，完整阅读：

1. [principles.md](references/principles.md)：全书原则及 MengDie Code 的适配结论。
2. [decision-gates.md](references/decision-gates.md)：每个切片必须通过的设计与验收门禁。

同时阅读仓库根目录的 `AGENTS.md`、`ARCHITECTURE.md` 和当前里程碑设计。发生冲突时，用户当前指令优先，其次是仓库已审核决策；本技能不得自行扩大范围。

## 核心工作流

### 1. 定位所处层次

先判断改动属于 Runtime、Framework、Harness 还是产品界面。MengDie Code 的主要职责是 Harness 和产品闭环；不要借机再造通用 Agent Framework，也不要提前稳定无调用方的 SDK。

### 2. 先定义上下文契约

明确哪些信息常驻、按需读取、落入 Artifact Store、进入短期状态或成为长期记忆。为大输出设置预算、截断、落盘指针、哈希和恢复方式。不得把“扩大 prompt”作为默认解决方案。

### 3. 先建立可观察计划

把复杂任务拆成有界步骤，保持最多一个 `in_progress`。计划必须能通过事件恢复，并在发现新证据时更新。简单任务不强制制造多余计划。

### 4. 选择最小自治级别

默认使用单 Agent 串行闭环。只有子任务独立、上下文噪声显著、结果可压缩且收益可评测时才引入子 Agent。异步任务还必须具备持久任务 ID、状态查询、追加指令、取消、恢复和资源上限。

### 5. 在副作用前强制安全

把 read、write、execute、network、external side effect 分开建模。Policy 和 Approval 必须位于模型调用与真实副作用之间。沙箱是执行隔离，不是授权系统；文件权限也不能约束 Shell、MCP 或自定义工具。

### 6. 把记忆当作带证据的数据

区分 thread-scoped 状态与 cross-thread 记忆。长期记忆必须带来源、作用域、时间、权威等级、状态和冲突链；召回只提供线索，不提高真实性。项目当前代码与命令结果优先于历史记忆。

### 7. 设计事件与可观测性

为模型、工具、审批、上下文压缩、记忆召回和成本产生结构化事件。持久事实先落库，再广播。任何恢复流程都不得把未知状态伪装成成功。

### 8. 用评测决定是否升级复杂度

实现前先写失败场景和验收证据。至少覆盖正常路径、拒绝路径、越界路径、中断恢复和跨平台差异。只有基线证明瓶颈存在，才升级到子 Agent、异步、向量、daemon 或更强沙箱。

## 不可妥协的约束

- 保持中文优先，macOS 与 Windows 都是一等平台。
- 保持模型与 Provider 可替换，不把兼容端点假设成行为完全一致。
- 不让提示词承担确定性权限职责。
- 不把 Skill、记忆或子 Agent 当作额外权限来源。
- 不在用户仓库自动提交，不覆盖未知或用户产生的修改。
- 不把摘要当作证据；保留退出码、测试结果、哈希和来源。
- 不让 Agent 自动修改正式策略、共享 Skill 或可信记忆；先生成提案并走审批。
- 不以“功能代码写完”代替端到端验收通过。

## 每个切片的交付证据

在设计或 PR 中明确记录：

- 用户痛点与可测指标；
- 上下文流向和持久化边界；
- 工具能力、风险等级与审批策略；
- 失败、中断、恢复和回滚行为；
- macOS、Windows 以及 CI 的验证证据；
- 本轮明确不做的能力；
- 引入依赖或复杂机制的收益证据。

未满足任一安全或恢复门禁时，停止扩大实现范围，先补设计、测试或证据。
