# P2-04B 会话事实订阅与 TUI 回放适配实施报告

> 状态：已实现，待 PR 审核  
> 日期：2026-08-10  
> 范围：已提交公开事实、同进程有界通知、`afterSeq` 补读与只读 TUI 适配

## 交付

Session Service 新增 `ReplayPublicFacts(sessionID, afterSeq, limit)` 与 `SubscribePublicFacts(sessionID, afterSeq)`。公开 `PublicFact` 不包含 Command 载荷、`CommandID`、私有上下文或存储实现细节；回放只投影 `visibility=public` 的已提交记录，并保持 `SessionSeq` 顺序。

`PublicFactPage.ThroughSeq` 会跨过被过滤的 private/metadata 记录。这样消费者既不会看到隐藏内容，也不会反复扫描同一段隐藏事实。`More` 采用保守语义：页长达到上限时再读一页，直到确认追上当前 EventStore 高水位。

## 提交、通知与背压

`EventSink` 保持固定顺序：

1. EventStore 原子提交；
2. 向 `PublicFactBus` 发布公开事实；
3. 交给现有 plain/JSONL Renderer。

存储失败时不会发布或渲染；Renderer 失败时已提交事实仍可恢复。总线是单进程、可丢弃的运行时通知层，默认每个订阅有 32 条缓冲。慢消费者不会阻塞 Agent：缓冲满时替换最旧通知，并在最新通知上标记 `Gap=true`；消费者随后从自己最后成功应用的 `SessionSeq` 补读。

关闭订阅是幂等操作，关闭与发布由同一互斥边界保护，不会向已关闭通道发送。当前没有 daemon、跨进程广播、多客户端协议或远程传输承诺；另一个 CLI 进程写入的事实仍以重新打开 EventStore 回放为准。

## TUI 边界与恢复

`internal/tui` 自己定义最小 `SessionFactSource` 接口，Application/Session Service 提供实现。Bubble Tea Model 不打开 SQLite，不访问 Provider、Policy 或工具。

启动顺序是“先订阅、再回放”，避免订阅建立期间丢失提交；回放期间到达的重复通知按 `SessionSeq` 忽略。收到显式 `Gap` 或发现序号不连续时，Model 不推断缺失状态，而是从 EventStore 补读后继续等待。退出会关闭订阅；回放错误会停止实时流并在界面显示可恢复提示，不影响已经持久化的会话。

本切片仍保持 `session tui` 只读。TUI 内命令提交、审批决策、diff 交互、跨进程实时通知与 daemon 不在 P2-04B 范围。

## 隐私

- private/metadata 记录只推进公开回放游标，不进入 `PublicFact`；
- `SessionView` Reducer 对非 public 记录只推进序号，不解释其 payload；
- 回归测试向 private/metadata payload 注入秘密文本，并验证公开回放与 SessionView 均不出现该文本；
- `message.delta` 继续保持瞬时，不写入 EventStore，也不进入本总线。

## 验证

- 单元测试覆盖 commit-before-publish、存储失败不通知、稳定分页、隐藏事实游标、隐私、慢消费者缺口、会话过滤、幂等关闭，以及 TUI 的订阅前回放、重复过滤、缺口补读和退出关闭；
- 未新增第三方依赖；沿用 P2-04A 已准入的 Bubble Tea v2 与 Lip Gloss v2；
- `go fmt ./...`、`go vet ./...`、`go test ./...`：通过，447 项 / 18 个包；
- `go test -race ./...` 与 `golangci-lint run --build-tags=liveprovider ./...`：通过；
- `govulncheck@v1.1.4 ./...`：未发现漏洞；Coding baseline：5/5；
- live-provider 离线 Harness：3/3；
- `CGO_ENABLED=0` 的 Windows amd64、macOS amd64/arm64、Linux amd64 构建通过。
