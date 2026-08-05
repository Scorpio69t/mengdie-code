# Policy 与 Approval 协议

本文记录第一阶段 Slice 06 的稳定安全边界。它约束后续 `edit_file`、`write_file`、`shell` 和 Agent Runtime 的接入方式，不代表这些后续能力已经实现。

## 决策顺序

`policy.Engine` 对同一个 `PreparedCall` 依次执行：

1. 硬拒绝：M1 网络 effect、项目根外路径、受保护路径写入，以及无交互模式下的敏感路径访问；
2. CLI 临时规则；
3. profile 规则；
4. 工具默认规则；
5. M1 安全默认值。

每层按声明顺序匹配，首条命中规则生效。无交互模式中的 `ask` 一律收敛为 `deny`，不会调用审批通道。普通项目读取默认允许；交互模式下的敏感读取、写入和执行默认询问；无交互模式默认拒绝这些操作。

硬拒绝不能被 CLI、profile 或工具规则放宽。`--allow-edit` 和 shell 精确 allowlist 后续通过窄规则装配，不改变硬边界。

## 调用与路径快照

工具 `Prepare` 必须把所有实际访问的规范绝对路径写入 `PreparedCall.Paths`，并保留 `PathGuard` 给出的 `Sensitive` 标记。Preview 只负责展示，Policy 不解析 Preview 文本做安全判断。

Policy 根目录和授权工作目录都必须先解析为同一真实目录；macOS 的 `/var`/`/private/var`、Windows 路径别名以及符号链接不能造成误判，也不能借此扩大授权范围。工作目录与 Policy 根不一致时，在显示审批前直接拒绝。

`PreparedCall` 的授权快照包含：

- Call ID、ToolName 与规范化参数；
- 参数 digest；
- effect 集合；
- 路径及敏感标记；
- 文件哈希等前置条件。

审批期间使用防御性副本。调用方即使随后修改原对象，也不能把已批准内容替换成另一个调用。

## 一次性 Capability

只有 `policy.Authorizer` 可以签发 Capability。它使用加密随机 nonce，默认有效期 30 秒，并绑定：

- RunID；
- ToolName；
- PreparedCall digest 和完整授权快照；
- 项目工作目录；
- 路径集合；
- effect 集合；
- 过期时间；
- 单次 nonce。

工具在任何外部读取或副作用之前调用 `tools.CheckCapability`。该函数拒绝空令牌和显然错配的令牌，然后由 Authorizer 在互斥区内完成全部字段校验与 nonce 消费。只有校验成功才消费授权；并发或重复执行最多一个成功。过期、伪造、跨 Run、跨 cwd、跨工具、参数/路径/effect/前置条件被修改的令牌全部失败。

“允许本次 Run 的所有编辑”只能表示一条窄的 CLI 临时规则；每个新 `PreparedCall` 仍需重新签发独立 Capability，不能复用旧 nonce。

## 审批通道

`ApprovalBroker` 只负责询问用户，不直接执行工具。M1 文本实现接受允许、拒绝、编辑三种结果：

- 允许：记录 `approval.resolved` 后签发 Capability；
- 拒绝：返回可识别的 Policy 拒绝错误，不签发 Capability；
- 编辑：返回 `ErrReprepare`，调用必须重新进入 Prepare 和 Policy；
- 事件写入失败或输入异常：安全失败，不签发 Capability。

终端输入有长度和尝试次数上限，审批事件只携带 Call ID、工具级提示、风险、决定和有界原因，不写入规范化参数、源代码或 diff。完整 Preview 由本地交互层展示。

## 接入约束

后续每个工具必须遵循同一顺序：

```text
Prepare（无副作用）
  → Policy Evaluate
  → 必要时 Approval
  → 签发一次性 Capability
  → Execute 首行校验并消费 Capability
  → 重新检查路径与前置条件
  → 执行外部读取或副作用
```

模型、提示词、CLI renderer 或 Tool 自身都不能绕过这条执行边界。
