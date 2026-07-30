# 第一阶段 Slice 03 实现报告

> 状态：本地验证通过，待远端 CI 与人工评审

## 目标

提供经过离线契约测试的 OpenAI-compatible `/chat/completions` 流式 Provider，不提前声称真实模型或 Agent Runtime 已可用。

## 交付

- 新增 `internal/provider` 最小外部边界：
  - `Provider`、`Capabilities`、`ChatRequest/Response` 与窄 `StreamEvent`；
  - assistant/tool message、function tool、tool choice 与参数 JSON 的网络前校验；
  - `invalid_request`、认证、权限、限流、超时、服务端、网络、协议、取消和 sink 稳定错误分类；
  - 错误对象不保留请求/响应正文、API key、完整 prompt、源码或隐藏 reasoning。
- 新增 `internal/provider/openaicompat`：
  - 基于标准库 `net/http` 的 `/chat/completions` client，不引入供应商 SDK 或新运行时依赖；
  - Base URL、Bearer header、超时、context 取消、`text/event-stream` 校验与请求/响应体关闭；
  - WHATWG 行语义的有界 SSE parser，支持 BOM、LF/CRLF、注释、多 `data:` 行和 `[DONE]`；
  - 文本流、usage/cache token 归一化，以及按 index 分片组装并校验 function tool calls；
  - 默认 2 MiB 单事件、8 MiB 累计响应上限，使用 builder 避免流式文本二次复制；
  - 默认 3 次、硬上限 5 次的指数退避加 jitter；仅重试 408/429/指定 5xx、transport 与可恢复断流，并在首个文本/工具增量后禁止重放；
  - 能力由配置显式声明，未声明能力在网络前失败，不调用 `/models` 猜测。
- 新增中文首要的 [Provider 协议](./PROVIDER_PROTOCOL.md)，同步 README 中英文入口、架构实现状态与依赖安全基线。
- Provider 引入真实 TLS/HTTP 可达路径后，`govulncheck` 暴露本机 Go 1.26.2 标准库漏洞；最低版本提升至官方已修复的 Go 1.26.5，再扫描为 0。

## 验证

本地验证（Windows，Go 1.26.5，race 时 `CGO_ENABLED=1`）：

- `go test ./...`：96 项通过；
- `go test -race ./...`：96 项通过；
- `go vet ./...`：通过；
- `govulncheck@v1.1.4 ./...`：未发现漏洞；
- `gofmt -l .` 与 `git diff --check`：无输出；
- `mengdie-eval --manifest evals/coding/smoke.json`：5/5 baseline 通过；
- Provider 包随机顺序连续运行 10 次：450 项通过（扩充累计响应测试前执行）；
- 契约覆盖请求路径/header/body、中文 Unicode 文本、CRLF、多 data 行、usage 与 cache token、工具片段、限额、畸形 JSON、finish 一致性、能力门禁、429 `Retry-After`、重试上限、断流前后语义、401 脱敏、取消和 sink 失败关闭响应体。

macOS、Windows 与 Ubuntu 使用同一纯 Go 协议实现；远端矩阵结果将在草稿 PR CI 后补充。

## 明确不做

- Agent Runtime 与工具执行；
- 真实 DeepSeek、Kimi、智谱联网 smoke；
- Responses API、Anthropic 原生协议与 reasoning stream；
- M2 EventStore、resume、上下文压缩与成本持久化。

## 已知限制

- `mengdie exec` 仍返回 `runtime_unavailable`，没有把已完成的 Provider 协议层伪装成可用 Agent；P1-09 才负责装配运行循环。
- DeepSeek、Kimi、智谱的真实 endpoint、系统代理、证书链与 capability probe 属于 P1-10 双平台 smoke。
- 首版只接受单 choice 的 `/chat/completions` SSE；不支持 Responses API、图片输入、原始 reasoning stream 或供应商私有内置工具。
