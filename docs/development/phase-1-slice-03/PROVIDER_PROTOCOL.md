# OpenAI-compatible Provider 协议

> 状态：P1-03，内部协议 v1

## 目的

`internal/provider` 是 Agent Runtime 与模型服务之间的最小外部边界。P1-03 只实现可配置 Base URL 的 `/chat/completions` 流式协议，不把任何供应商 SDK、终端 UI、工具执行或 Agent 状态带入 Provider。

当前实现经过本地 `httptest` 契约验证，但尚未接入 `mengdie exec`。真实 CLI 调用、配置装配和 DeepSeek/Kimi/智谱实机 smoke 分别属于 P1-09/P1-10；在这些工作完成前，README 不宣称模型已可直接使用。

## 公开边界

```go
type Provider interface {
    ID() string
    Capabilities(context.Context, string) (Capabilities, error)
    Stream(context.Context, ChatRequest, StreamSink) (*ChatResponse, error)
}
```

首版能力必须由适配器配置显式声明：

| 能力 | 含义 |
|---|---|
| `ToolCalling` | 请求和响应可以包含 function tool calls |
| `ParallelTools` | 可以发送 `parallel_tool_calls=true` |
| `UsageInStream` | 可以请求流式 usage chunk |
| `StrictToolSchema` | 可以发送 function `strict=true` |
| `MaxContextTokens` | 配置声明的最大上下文；本切片不自行探测 |

请求使用未声明能力时，在发起网络请求前返回稳定的 `invalid_request`。P1-03 不调用 `/models` 猜测能力；真实 capability probe 留给 P1-10。

P1-03 也不支持配置附加静态 header（如网关租户头）。第一阶段详细设计 §7.2 中的“附加静态 header”能力随 P1-09 的配置装配交付，届时再按真实端点需求评估。

## 流事件

Provider sink 只接收以下窄事件，后续 Agent bridge 再映射到 M1 Event 协议：

| kind | 内容 | 是否影响自动重试 |
|---|---|---|
| `text.delta` | 文本增量 | 是 |
| `tool_call.delta` | 按 index 标识的 ID、函数名、参数片段 | 是 |
| `usage` | 输入、输出、总计与 cache read token | 否 |
| `finished` | 非空 finish reason | 否 |
| `retry` | 下一次尝试、上限、等待时间和错误类别 | 不适用 |

工具调用按 `index` 独立组装，结束时必须得到唯一非空 ID、函数名和有效 JSON 参数。存在工具调用时 finish reason 必须为 `tool_calls`。reasoning/thinking 扩展字段不会进入领域响应、流事件或错误。

## SSE 约束

- 支持 UTF-8 BOM、LF/CRLF、注释行、多 `data:` 行和 `[DONE]`；
- 遵循空行分发事件的语义，EOF 时丢弃未由空行结束的 pending event；
- 默认单事件上限 2 MiB，可配置范围上限 16 MiB；
- 累计文本与工具调用片段上限为单事件上限的 4 倍（默认 8 MiB），避免小事件无限堆积；
- 只接受单 choice、`index=0`，未知 JSON 字段忽略，核心完成语义严格校验；
- 2xx 响应必须声明 `text/event-stream`，否则为 `protocol` 错误。

## 错误与重试

稳定类别为：`invalid_request`、`authentication`、`permission`、`rate_limit`、`timeout`、`server`、`network`、`protocol`、`canceled`、`sink`。

默认最多尝试 3 次，硬上限 5 次。只有下列情况可重试：

- HTTP 408、429、500、502、503、504；
- transport timeout 或 network error；
- 未收到 finish 的意外断流。

重试还必须同时满足“尚未向 sink 交付文本或工具增量”。一旦产生模型可见增量，断流会返回 `unexpected_eof`，绝不自动重放，避免重复文本或重复工具调用。退避采用指数延迟加加密随机 jitter，优先尊重 `Retry-After`，整体仍受请求 context 与默认 120 秒超时约束。

401、403、协议错误、输入错误和 sink 错误不重试。每次重试都重新构造请求体；响应体在成功、失败、取消和 sink 拒绝路径上都会关闭。

## 隐私边界

- Authorization 只进入 HTTP header；Base URL 禁止 userinfo、query 和 fragment；
- 错误仅保留类别、HTTP 状态、安全错误码、request ID、重试属性和底层 transport 错误；
- HTTP 错误响应正文、完整请求体、prompt、源码、API key 与 reasoning 内容不进入错误；
- Provider 不自行记录事件或请求。上层若增加诊断日志，必须继续遵守同一边界。

## 跨平台说明

实现只依赖 Go 标准库 `net/http`、`bufio` 与 `context`，没有 CGO、shell 或平台路径分支。macOS 与 Windows 共用同一协议实现和契约测试；真实网络、系统代理和证书链差异将在 P1-10 双平台 smoke 中验证。
