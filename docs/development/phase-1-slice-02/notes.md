# Notes：第一阶段 Slice 02

## 输入约束

- 用户已审核并合并 Deep Agent 指导 Skill，授权继续开发。
- 当前工作包：P1-02 事件与终端 renderer。
- 目标平台：macOS、Windows 一等支持；Linux CI 保持完整。
- 不进入 P1-03 Provider，除非 P1-02 完成并单独重新决策。

## Deep Agent 门禁映射

- Context：renderer 不持有会话正文，只消费有界事件 payload。
- Planning：事件序列必须承载 todo 与 run 状态，最多一个进行中 todo 的约束留在 Planner。
- Safety：不显示隐藏推理；JSON 编码失败和输出写失败必须显式返回。
- Recovery：M1 只保证 RunID + Seq + Version 的可排序语义，不声称支持重放或 resume。
- Evaluation：覆盖正常输出、未知事件、并发 emit、writer 失败、双 Ctrl+C 和窗口过期。

## Findings

- 当前 `internal/app` 直接打印占位文本，`exec --json` 使用临时 map；P1-02 应把两者改为消费同一事件协议。
- `cmd/mengdie` 原先使用 `context.Background()` 且没有 signal handler；本切片已改为注入 operation context，并在入口安装可测试的双 Ctrl+C 监听。
- `internal/brand` 只负责欢迎屏，保持不变；颜色和完整 TUI 继续后置。
- M1 事件集合采用设计稿中的 run/message/todo/tool/approval/usage/warning 事件，并补充稳定 schema version。
- 人类 renderer 只解码已知、显示安全的字段；未知事件只显示 kind，不倾倒 raw payload。
- JSON Lines 保留结构化 payload，事件生产方不得把密钥、隐藏推理或完整 prompt 写入事件。
- 使用串行 `Emitter` 分配 Run 内单调 Seq；并发调用也按 Sink 接收顺序稳定递增。
