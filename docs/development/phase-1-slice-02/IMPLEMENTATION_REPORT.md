# 第一阶段 Slice 02 实现报告

> 状态：本地与远端验证通过，待人工评审

## 目标

交付 P1-02 的事件与终端展示边界，不提前声称 Provider 或 Agent Runtime 可用。

## 交付

- 新增 `internal/events`：
  - 固定 M1 的 14 类事件名与版本 1 envelope；
  - `RunID + Seq + Version + Time + Kind + Payload` 保持 UI 无关；
  - 串行 `Emitter` 在并发调用下仍按 Sink 接收顺序分配单调序号；
  - `MemorySink` 只服务进程内消费与测试，不承担持久化或恢复；
  - RunID 使用系统加密随机源生成 128-bit 不透明标识。
- 新增 `internal/ui/terminal`：
  - 人类 renderer 输出稳定、无颜色依赖的中文文本；
  - JSON renderer 严格每个事件一行；
  - 未知事件只展示 kind，不倾倒未知 payload；
  - context、schema 与 writer 错误全部向调用方传播。
- 新增 Ctrl+C 状态机与监听器：
  - 第一次中断调用 `cancelCurrent`；
  - 2 秒内第二次中断调用 `exitProcess`；
  - 时钟、信号通道与回调均可注入，测试不依赖 sleep；
  - CLI 入口已监听 `os.Interrupt`，普通取消使用退出码 5，第二次强制退出使用 130。
- `mengdie exec` 已改用统一事件协议：
  - `--json` 把 JSON Lines 写入 stdout；
  - 默认人类输出写入 stderr；
  - 当前只输出 `run.started` 与 `run.failed(runtime_unavailable)` 并返回退出码 1，不伪装 Runtime 已可用。
- 事件隐私边界：`run.started` 不包含原始任务；生产方不得写入密钥、隐藏推理或完整 prompt。

协议细节见 [EVENT_PROTOCOL.md](./EVENT_PROTOCOL.md)。

## 验证

本地验证（Windows 11，`CGO_ENABLED=1`）：

- `go test ./...`：43 项通过；
- `go test -race ./...`：43 项通过；
- `go vet ./...`：通过；
- `gofmt -l .` 与 `git diff --check`：无输出；
- `govulncheck ./...`：当前代码调用链 0 个已知漏洞；
- `mengdie-eval --manifest evals/coding/smoke.json`：5/5 baseline 通过；
- `mengdie exec --cwd . --json "验证事件输出"`：恰好输出两条可解析事件，未包含任务正文，并按设计以退出码 1 结束；
- 覆盖并发序号、无效 envelope、RunID、JSON Lines、未知 payload、防泄露、writer 失败、context 取消、双 Ctrl+C、窗口过期与 TTY/pipe 输出分流。

远端 PR #7 首轮 CI：macOS、Windows、Ubuntu 与质量检查全部通过。

## 明确不做

- OpenAI-compatible Provider；
- Agent Runtime 与工具执行；
- M2 EventStore、session resume 与完整 TUI。
- macOS 进程组与 Windows Job Object 的子进程树终止；该能力属于 P1-08。

## 已知限制

- 当前交互模式仍是品牌与配置预览，不存在持续 REPL；第一次 Ctrl+C 已能取消当前 App context，待 P1-09 引入真实操作循环后再绑定到单次模型/工具操作并保留 CLI。
- JSON Lines 是版本化协议边界，不是持久化日志；进程退出后不会恢复。
