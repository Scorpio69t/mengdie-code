# Notes：第一阶段 Slice 03

## 输入约束

- 用户已审核并授权合并 P1-02，要求继续开发。
- 当前工作包仅为 P1-03 OpenAI-compatible Provider。
- macOS、Windows 为 Tier 1；契约测试不得依赖真实网络或付费 API。
- HTTP/SSE 使用 Go 标准库，保持单二进制与可替换边界。

## Deep Agent 门禁映射

- Context：Provider 接收有界 `ChatRequest`，不记录或复制完整 prompt 到诊断事件。
- Planning：本包不持有 Agent todo；只保证流事件顺序和错误可观察。
- Safety：Authorization 只写请求 header；错误、重试和测试不得回显 token。
- Recovery：可见 delta 后断流不自动重放；调用方收到明确 `unexpected_eof` 或取消错误。
- Evaluation：覆盖文本、Unicode、tool fragments、usage、CRLF、多 data、超限、429、5xx、断流、取消和凭据脱敏。

## Findings

### 官方协议交集（2026-07-30 核验）

- [OpenAI Chat Completions API](https://developers.openai.com/api/reference/resources/chat)：流块使用 `choices[].delta`；tool call delta 具有 `index`、可选 `id`、`function.name` 与 `function.arguments`；启用 `include_usage` 时最后一个块可以是空 choices + usage。
- [DeepSeek Chat Completion](https://api-docs.deepseek.com/api/create-chat-completion/)：`stream=true` 使用 data-only SSE 并由 `data: [DONE]` 终止；支持 function tools 和 `tool_calls` finish reason；另外提供 reasoning 与 prompt cache 扩展。
- [智谱对话补全](https://docs.bigmodel.cn/api-reference/模型-api/对话补全)：使用 Bearer + `/chat/completions`，支持 SSE、usage 与工具调用；部分模型的流式工具调用需要供应商扩展开关。
- [WHATWG Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html)：空行派发事件，注释行忽略，多 `data:` 行以 LF 拼接，EOF 前未闭合事件丢弃。

### 兼容策略

- 只编码 `/chat/completions` 共同字段；未知 JSON 字段宽松忽略。
- reasoning/thinking 字段不进入领域事件、响应、日志或错误。
- `stream_options.include_usage`、parallel tools 与 strict schema 只有显式 capability 为真时才允许请求。
- 不用 `/models` 猜测工具能力；P1-03 返回配置声明的 capability，真实 probe 留给 P1-10。
- 单次 SSE 事件默认上限 2 MiB；使用 `bufio.Reader` 的有界行读取，不使用默认上限不足的 `bufio.Scanner`。

### 仓库审计

- `config.Profile` 已提供 provider、base URL、API key 环境变量名、model、timeout 与 context tokens，足以在后续 App 装配 Client。
- P1-02 `events` 是产品 Harness 事件；P1-03 需要更窄的 Provider `StreamEvent`，由后续 Agent bridge 映射为 message/usage/warning 事件，避免 Provider 依赖 UI。
- 当前仅有 TOML 直接依赖；HTTP/SSE 用标准库即可，不新增组件或供应链成本。
