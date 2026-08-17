# P2-08A 实施报告：可恢复用量事实与版本化成本估算

## 交付范围

本切片把模型用量从“最后一次观察值”改为“每次逻辑请求一条公开事实”，覆盖主 Agent 与上下文摘要两类调用：

- 每次 `Provider.Stream` 返回后提交一条 `usage.updated`，固定 `request_count=1`，并标记调用目的；
- 流式 usage 只收集最终观察值，不在流中重复落盘；相邻请求即使用量完全相同也不会被误去重；
- Provider 失败或取消时，仍在终态事件前提交请求事实；未观察到 usage 时明确记录 `usage_unreported`；
- RunResult、Session reducer、快照/重放、JSON、普通 CLI 与 TUI 使用同一组确定性聚合字段；
- P2-08A 之前的历史 `usage.updated` 仍按 token 聚合，但不反推不存在的请求数或成本。

P2-08B 继续承担随机 kill、双平台真实 Provider 验收与完整 M2 退出报告，本切片不提前宣称 M2 完成。

## 成本口径

成本估算只在规范化 endpoint origin 与精确模型名同时命中时启用。价表版本为 `2026-08-17`，当前仅收录 DeepSeek 官方定价页明确列出的精确模型：

| Origin | 模型 | 缓存未命中输入 | 缓存命中输入 | 输出 |
| --- | --- | ---: | ---: | ---: |
| `https://api.deepseek.com` | `deepseek-v4-flash` | $0.14 / 1M tokens | $0.0028 / 1M tokens | $0.28 / 1M tokens |
| `https://api.deepseek.com` | `deepseek-v4-pro` | $0.435 / 1M tokens | $0.003625 / 1M tokens | $0.87 / 1M tokens |

来源：[DeepSeek API Docs - Models & Pricing](https://api-docs.deepseek.com/quick_start/pricing)。官方价格可能变化，因此任何价格调整、模型增删或来源变化都必须更新本地表版本。

内部统一使用 pico-USD（$10^-12）整数计算，不使用浮点数。结果始终标记为“估算”，不冒充 Provider 账单；未知端点、别名、未收录模型、无 usage、无效 usage 或整数溢出均明确显示 unknown 及原因，不以 0 美元冒充免费。Kimi 当前保持 unknown，直到有经过审核的官方精确模型价表。

## 恢复、安全与隐私

`usage.updated` 与其他公开事实一样先进入 Session EventStore，再更新运行时内存聚合。公开负载只包含用途、请求数、token、规范化 origin、精确模型和价表元数据；端点路径、查询参数、URL 用户信息、API Key、任务与响应正文不会进入用量事实。

Session reducer 对负数、cache-read 大于 input、非法目的、错误成本状态和累计溢出 fail-closed。旧事件的新增字段缺失是兼容状态，不会被误判成一次新请求。

## 验证

测试覆盖正常请求、跨轮相同 usage、上下文摘要、Provider 失败前已观察 usage、未上报 usage、unknown price、精确模型错配、整数计算与溢出、历史事件兼容、快照/重放、公开端点脱敏，以及普通 CLI、JSON 与 TUI 展示。

本地验证结果：

- `go fmt ./...`：通过；
- `go vet ./...`：无问题；
- `go test ./...`：20 个 package、552 项测试通过；
- `go test -race ./...`：20 个 package、552 项测试通过；
- `golangci-lint run ./...`：0 issue；
- `govulncheck@v1.1.4 ./...`：未发现漏洞；
- `darwin/arm64`、`darwin/amd64`、`windows/amd64`、`linux/amd64` 的 `CGO_ENABLED=0 go build ./cmd/...`：全部通过。
