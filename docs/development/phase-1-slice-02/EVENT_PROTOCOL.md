# M1 事件与终端协议

> 状态：P1-02，schema version 1

## 目的

Provider、Agent、工具和 UI 通过事件边界协作。事件生产方不直接打印，终端、人类输出、JSON Lines 与未来 TUI 可以消费同一条有序事件流。

M1 只在内存中传递事件。此协议不提供 EventStore、重放、session resume 或跨进程顺序保证。

## Envelope

```json
{"run_id":"run_0123456789abcdef0123456789abcdef","seq":1,"version":1,"time":"2026-07-30T09:00:00Z","kind":"run.started","payload":{"model":"openai-compatible:deepseek-chat","cwd":"/project","security":"建议模式"}}
```

| 字段 | 约束 |
|---|---|
| `run_id` | 单次运行内不变，使用本地加密随机源生成 |
| `seq` | 从 1 开始，按 Sink 接收顺序单调递增 |
| `version` | 当前固定为 `1`，未知版本拒绝渲染 |
| `time` | UTC 时间 |
| `kind` | 稳定事件名；人类 renderer 对未知 kind 只显示名称 |
| `payload` | kind 对应的 JSON 对象；不得包含密钥、隐藏推理或完整用户 prompt |

## 事件集合

| 类别 | 事件 |
|---|---|
| Run | `run.started`、`run.completed`、`run.failed`、`run.cancelled` |
| Message | `message.delta`、`message.completed` |
| Planning | `todo.updated` |
| Tool | `tool.proposed`、`tool.started`、`tool.completed` |
| Approval | `approval.needed`、`approval.resolved` |
| Telemetry | `usage.updated`、`warning` |

Go payload 类型以 [`internal/events/event.go`](../../../internal/events/event.go) 为准。新增字段应优先采用可选字段；破坏性变更必须提升 schema version，并同时更新两种 renderer 与契约测试。

## 输出约定

- `mengdie exec --json`：完整事件以 JSON Lines 写入 stdout，每行恰好一个事件；诊断错误写入 stderr。
- `mengdie exec`：面向人的中文事件写入 stderr，为未来把最终模型文本独占 stdout 保留稳定管道。
- 交互欢迎 Logo 只在 stdout 为 TTY 时显示；重定向时不注入装饰内容。
- renderer 必须返回 context、schema 和 writer 错误，不能静默丢事件。

当前 `exec` 只是协议预览：输出 `run.started` 后输出 `run.failed`，退出码为 1。Agent Runtime 将在后续工作包实现。

## 中断约定

- 第一次 Ctrl+C：调用当前操作的取消回调；普通用户取消退出码为 5。
- 2 秒内第二次 Ctrl+C：调用强制退出回调；CLI 入口使用传统退出码 130。
- 操作成功、失败或切换时，调用方应重置状态机。
- P1-08 才负责 macOS 进程组与 Windows Job Object 的子进程树清理。
