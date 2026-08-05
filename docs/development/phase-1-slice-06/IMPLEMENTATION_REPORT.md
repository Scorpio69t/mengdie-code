# 第一阶段 Slice 06 实现报告

> 状态：本地实现与安全回归完成，待代码审核和远端 CI。

## 目标

交付确定性 Policy、交互 Approval 和一次性 Capability，把授权从“调用方携带一个看似有效的结构体”升级为工具执行边界上的真实权限校验。明确不提前实现修改工具、shell 或 Agent Runtime。

## 交付

- `internal/policy`：
  - 固定优先级的规则引擎：硬拒绝 > CLI 临时规则 > profile 规则 > 工具默认规则；
  - 交互/无交互默认矩阵，无交互 `ask` 安全收敛为 `deny` 且不触发 Broker；
  - 项目外、受保护路径写入、M1 网络 effect、无交互敏感读取的不可覆盖硬拒绝；
  - `Authorizer` 防御性快照、30 秒默认 TTL、256 bit 加密随机 nonce；
  - 完整 grant 绑定与互斥区原子消费，伪造或错配不会烧毁真实授权，成功授权只能消费一次；
  - `ApprovalBroker` 三态结果：允许、拒绝、编辑后重新 Prepare；
  - 中文优先文本审批，输入大小与无效尝试次数有界；
  - `EventObserver` 接入既有 `approval.needed` / `approval.resolved`，事件失败时不签发授权。
- `internal/tools`：
  - `PreparedCall.Paths` 保存规范绝对路径和敏感标记，Policy 不依赖 Preview 文本猜测资源；
  - `ExecEnv` 增加 RunID 与 `CapabilityVerifier`；
  - `CheckCapability` 必须调用真实 Authorizer 完成快照、过期和 nonce 验证，非空 nonce 不再被视为权限；
  - 三个只读工具在 Execute 读取文件系统前完成一次性 Capability 消费；
  - PreparedCall 增加路径唯一性、路径规范性与 Preview 预算检查。
- 文档：新增 [Policy 与 Approval 协议](./POLICY_PROTOCOL.md)，并同步中英文 README 进度。
- 无新增第三方依赖。

## 安全验证

本地环境为 Windows、Go 1.26.5：

- `go test ./...`：14 个包、245 项测试通过；
- `go test -race ./internal/policy ./internal/tools`：110 项定向测试通过；
- `go vet ./...`：通过；
- `gofmt -l .` 与 `git diff --check`：无输出。
- `mengdie-eval --manifest evals/coding/smoke.json --pretty`：5/5 baseline 通过。

定向回归覆盖规则默认矩阵与四层优先级、硬拒绝不可覆盖、无交互不调用 Broker、审批允许/拒绝/编辑、审批事件前后失败、事件敏感信息不泄漏、超长和无效输入、拒绝零副作用、真实 `read_file` 授权执行，以及 Capability 的全部安全绑定。RunID、工具、规范参数、digest、cwd、路径和敏感标记、effect、前置条件、过期时间与 nonce 均有篡改用例；32 路并发消费只有一次成功。

macOS、Windows 与 Linux 由现有三平台 CI 继续验证。实现只使用 Go 标准库路径、同步、随机数和终端 I/O 接口，没有引入平台专用授权逻辑。

## 明确不做

- `edit_file` / `write_file` 与 Patch Journal（P1-07）；
- `shell`、进程组和精确命令 allowlist 装配（P1-08）；
- Provider、Tool、Policy 的 Agent Runtime 主循环接线（P1-09）；
- daemon、Web、异步 Swarm、向量记忆或记忆图谱；
- 把一次批准扩大为可复用 Capability。

## 后续接入要求

P1-07 与 P1-08 的每个 Execute 实现必须先调用 `tools.CheckCapability`，再重新解析路径、检查前置条件，最后才产生副作用。P1-09 负责从已加载配置和 CLI 参数组装规则层、Broker、Observer 和 ExecEnv；它不能复制 Capability 签发逻辑，也不能在 renderer 或模型提示词层模拟授权。
