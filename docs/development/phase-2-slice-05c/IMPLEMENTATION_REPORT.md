# P2-05C 滚动摘要来源受控按需回填实施报告

> 状态：已实现，待 PR 审核
> 日期：2026-08-11
> Beads：`mengdie-28a`

## 用户痛点与切片边界

P2-05B 能把长历史压成可验证滚动摘要，但模型看到“需要精确内容时回查原始上下文”时没有真正可调用的入口。摘要一旦遗漏命令参数、错误原文或文件名，模型只能猜测或重新执行工具；直接重新注入整个来源区间又会抵消 token 预算。

本切片新增 `read_context_source`，只补齐“当前 Session 的最新有效摘要 → 有界精确原文”闭环。它不是数据库浏览器、Artifact 任意读取器或跨会话搜索工具，不接受 Session ID、项目路径、绝对 ordinal 或任意来源区间，也不增加文件、命令和网络权限。

## 工具协议与授权边界

模型输入只有三个整数：

- `offset`：摘要来源区间内从 0 开始的相对消息偏移；
- `limit`：本页最多消息数，缺省和上限均为 4；
- `byte_offset`：超大消息的规范 JSON 字节续读位置；使用时 `limit` 必须为 1。

Prepare 先读取最新摘要描述并验证区间，把模型不可控的 `summary_sha256`、`source_start`、`source_end` 写入内部规范参数。调用摘要和一次性 Capability 因此同时绑定分页意图与当前摘要身份。Execute 消费 Capability 后，再要求 Session Reader 以同一身份加载；若期间产生了更新摘要，旧调用以 `ErrContextSummaryChanged` 拒绝，不能静默扩大授权范围。

工具声明唯一 `read` effect，不携带路径资源。Policy 仍在统一 Authorizer 边界签发和消费 Capability；工具没有绕过策略的特殊执行通道。没有滚动摘要、offset 越界、非法分页、取消或结果超过上限时均明确失败。

Runtime 在尚无滚动摘要时不会把该工具加入 Provider 请求，避免无效调用和常驻 schema token；首次压缩后的主请求以及携带有效摘要的 Resume 才会声明该工具。压缩规划使用包含该 schema 的预算上界，确保新增工具定义不会让压缩后的请求重新超限。

## 有界分页与大消息

每页最多 4 条消息，原文载荷预算 6 KiB，最终工具输出硬上限 16 KiB。每条结果包含：

- 摘要 SHA-256 与来源 `source_start/source_end`；
- 相对 offset、绝对 ordinal、role 与 `full/sanitized` 完整性；
- 原始消息 SHA-256；
- 规范 `provider.Message` JSON 的精确字节片段、片段起止位置与总大小；
- `has_more`、`next_offset` 与 `next_byte_offset`。

小消息通常完整返回；单条消息超过本页预算时只返回 UTF-8 边界上的 JSON 字节片段，模型按返回指针继续。这样即使原始消息由 Artifact Store 承载，也不会一次把整个压缩区间重新塞回上下文。

## 完整性、恢复与隐私

Session 层新增带摘要身份的来源加载入口。它先复验最新摘要协议、摘要 SHA 与来源连续性，再通过既有 `LoadContext` 复验全量私有消息顺序、角色、消息 SHA、Artifact 根锚定路径、文件类型、大小和 SHA-256，最后只切出摘要覆盖的 `source_start..source_end`。

回填保持 P2-03B/P2-05B 的恢复安全边界：user、assistant 与只读工具可返回完整持久事实；write、execute、network 工具只可能返回此前写入私有账本的 `sanitized` 摘要。回填工具结果作为 read-only 模型消息进入私有 Context Ledger，便于安全 resume；公开 `tool.proposed/started/completed` 只包含工具名、摘要短标识、ordinal 区间和执行结果，不包含回填正文。TUI、JSONL 与 EventBus 不成为第二份私有事实源。

## 验证

- 工具单测覆盖正常分页、规范参数绑定、超大消息续读、非法 offset/limit、摘要轮换、摘要缺失与取消；
- Session 集成测试覆盖只返回摘要来源区间、`sanitized` 保持、消息 SHA 篡改与 Artifact 文件篡改；
- Runtime/Context Builder 回归确认压缩前不声明无效工具、压缩规划计入 schema 预算，压缩后明确指导并声明有界回填工具；
- `go fmt ./...`、`go vet ./...`、`go test ./...`：通过，499 项 / 18 个包；`go test -race ./...` 同样通过；
- `golangci-lint run --build-tags=liveprovider ./...`：0 issue；`govulncheck@v1.1.4 ./...`：未发现漏洞；
- Coding baseline：5/5；live-provider 离线 Harness：3/3；
- `CGO_ENABLED=0` 的 Windows amd64、macOS amd64/arm64、Linux amd64 构建通过。
