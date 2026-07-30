# Deep Agent 原则与 MengDie Code 适配

## 来源与边界

本参考基于 Datawhale China《Deep Agents 实战》截至 2026-07-30 已发布的 2 篇准备篇和 11 章正文：

- 课程站点：<https://datawhalechina.github.io/deepagents-in-action/>
- 原始 Markdown：<https://github.com/datawhalechina/deepagents-in-action/tree/main/content>

课程讲解的是 Deep Agents / LangChain / LangGraph 生态。MengDie Code 是 Go 实现、本地优先、模型可替换的 Coding CLI，因此只吸收可迁移的架构原则；版本号、Python 类名、Provider API 和默认参数必须在真正采用时重新核验官方文档。

## 全书结论

### 准备篇：生命周期与开发技能

- 用可检查的生命周期命令统一创建、依赖、配置、诊断、启动和验证。
- 把密钥留在环境或凭据系统，不写入配置和版本库。
- 项目级 Skill 随仓库共享，全局 Skill 服务个人复用；安装、更新和移除路径必须可见。
- Trace 分析先定位完整任务，再按 run type、name 和层级缩小，不把包装节点误判成瓶颈。

**MengDie 适配：** `doctor`、配置优先级、项目级技能发现和事件追踪属于产品能力，不依赖特定模板工具。

### 第 1 章：Runtime → Framework → Harness

- Runtime 负责持久执行、流式、状态和中断。
- Framework 负责模型、工具、循环和 Middleware 抽象。
- Harness 把文件、规划、子 Agent、记忆、Skills、安全和可观测性组成可用产品。
- Context Engineering 比堆叠 prompt 更重要：只把当前需要的信息交给模型。

**MengDie 适配：** 聚焦 Harness 与 Coding 产品闭环，复用成熟协议和库；不要把项目演变成通用 Agent Framework。

### 第 2 章：工具与模型接入

- 工具契约需要清晰名称、类型 schema、行为描述和稳定返回。
- OpenAI-compatible 只是协议入口，不代表工具流、错误、用量或 JSON 行为一致。
- 真实 Agent 行为必须可追踪到模型调用和工具调用。

**MengDie 适配：** Provider 采用最小统一层和能力探测；工具 schema、错误分类、usage 和原始事件都要保留。

### 第 3 章：虚拟文件系统与上下文工程

- 按需 `ls/read/grep/glob`，不要一次加载全库。
- 大结果卸载为文件或 Artifact，只在上下文保留路径、摘要和关键片段。
- 对话接近预算时结构化压缩，但保留原始记录用于恢复和审计。
- Backend 决定数据在哪里；权限和策略决定是否允许访问。
- 本地 Shell 后端不是沙箱，路径根目录也不自动等于越界保护。

**MengDie 适配：** 真实项目文件与会话 Artifact Store 分开；输出限制、哈希、生命周期、清理和恢复都是上下文契约的一部分。

### 第 4 章：任务规划与 Middleware

- 复杂任务先拆解，状态采用 `pending → in_progress → completed`。
- Todo 是压缩后仍应保留的任务锚点；最多一个进行中步骤。
- Middleware 的节点式 Hook 适合状态、审计和中断，包装式 Hook 适合重试、缓存和转换。

**MengDie 适配：** Todo 更新必须形成事件并可恢复；简单任务不强制规划，复杂任务必须能解释计划变化。

### 第 5 章：子 Agent 与 Context Quarantine

- 子 Agent 的首要价值是上下文隔离，不是角色数量。
- 只委派独立、上下文密集、可给出精炼结果的任务。
- 描述必须能支持准确路由；工具集遵循最小权限；结果优先结构化并限制体积。
- 自定义子 Agent 的继承关系必须显式，不能假设自动继承 Skills、权限或工具。

**MengDie 适配：** v0.1 不引入子 Agent；只有单 Agent 基线证明上下文或并行瓶颈后才进入 M5 验证。

### 第 6 章：异步子 Agent

- 异步不是“开 goroutine”：需要持久 task ID、状态查询、追加指令、取消、恢复和独立元数据通道。
- 任务状态不能从旧消息推断，回答进度前必须读取最新状态。
- Worker 槽位、排队、部署拓扑和 trace 关联属于正确性的一部分。

**MengDie 适配：** daemon、后台子任务和异步 Swarm 留在评测证明价值之后；若实现，JobStore 而非内存任务是事实源。

### 第 7 章：Skills 与渐进式披露

- Skill 是 `SKILL.md` 加可选 scripts、references、assets 的开放能力包。
- 三级加载：元数据常驻、正文命中后加载、资源按需读取或执行。
- `description` 决定匹配质量；正文保持短、步骤化、有分支标准。
- 多来源同名 Skill 要有清晰优先级；共享 Skill 应只读或修改需审批。
- Skill 是程序性指导，不提供额外工具权限。

**MengDie 适配：** M2 实现用户级与项目级 Skill，项目级优先；解析、发现、冲突和 token 成本需要测试。

### 第 8 章：长期记忆

- Checkpointer/状态是 thread-scoped 短期记忆；Store 是 cross-thread 长期记忆。
- namespace 必须隔离用户、项目、Agent 或组织。
- 按主题拆文件或记录，减少并发写冲突；热路径写入与后台整合分离。
- 组织策略和共享知识默认只读；记忆写入必须可审计。

**MengDie 适配：** 在课程原则上增加可信性约束：来源、权威、时间、作用域、状态和冲突；召回不等于证实，仓库事实优先。

### 第 9 章：Human-in-the-Loop

- 高风险副作用在执行前中断，支持 approve、edit、reject；“由人提供工具结果”只适合提问类工具。
- 中断恢复依赖持久状态和相同会话标识。
- 拒绝反馈要说明原因和下一步，避免模型原样重试。
- 中断点应放在可重放边界；恢复可能从节点开头重放，因此副作用必须幂等。
- 并行中断用稳定 ID 关联决定，不能依赖显示顺序。

**MengDie 适配：** ApprovalBroker、Command 幂等键、Capability Token 和 EventStore 共同定义可恢复审批。

### 第 10 章：沙箱执行

- 沙箱 Backend 提供隔离文件系统和 execute，但仍不是授权或内容信任机制。
- “Agent 文件工具平面”和“宿主上传/下载平面”必须分离。
- 选择 thread-scoped 或 assistant-scoped 生命周期，并配置 TTL、配额和清理。
- 凭据优先留在宿主侧；沙箱产物默认不可信，使用前审查。
- 不需要联网时关闭网络；记录命令、文件操作和生命周期事件。

**MengDie 适配：** macOS/Windows v0.1 如无可靠 OS 隔离，必须显示“本地受控执行”，不得宣传为强沙箱。

### 第 11 章：文件系统权限

- 权限规则必须明确 operation、path、mode、顺序和默认行为。
- 白名单需要闭合：具体敏感拒绝 → 允许工作区 → 拒绝其余。
- 目录删除必须全有或全无，不能部分成功。
- 子 Agent 显式权限通常是整体替换，必须独立闭合。
- 文件权限只覆盖文件工具；Shell、MCP、自定义工具和网络需要各自策略。
- 上线前同时测试允许、拒绝、越界、路径规范化、符号链接/重解析点和中断恢复。

**MengDie 适配：** 权限语义必须由 PolicyEngine 统一并跨平台契约测试，不能依赖模型自律或某个 Backend 的隐式行为。

## 战略公理

1. 先证明用户闭环，再增加自治复杂度。
2. Context 是预算化的数据流，不是一段无限增长的聊天记录。
3. 持久状态是事实源，内存流和 UI 都可重建。
4. 计划、任务 ID、审批和未解决错误必须跨压缩保留。
5. 委派的价值用上下文隔离和可测并行收益衡量。
6. Skill、Memory、Tool 分工明确：程序、事实、能力不可混为一谈。
7. 任何副作用都经过确定性 Policy；沙箱只提供额外隔离。
8. 记忆必须能回答“从哪里来、对谁有效、何时失效、如何纠正”。
9. Trace、事件、成本和评测是产品能力，不是上线后补的运维附件。
10. 高级机制只有在基线评测显示净收益时才进入路线图。
