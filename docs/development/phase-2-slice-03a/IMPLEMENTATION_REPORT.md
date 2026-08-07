# P2-03A Command Ledger、Reducer/Snapshot 与会话查看实施报告

> 状态：已实现，待 PR 审核  
> 日期：2026-08-07  
> 范围：Command 幂等账本、公开 SessionView、可丢弃 Snapshot、会话 CLI

## 1. 交付结论

本切片把一次运行拆为独立的 Session、Command 和 Run 身份，并在 Provider、工具与项目指令初始化前原子登记 Command。调用方提供 `--command-id` 时，同 ID、同规范载荷只回放已提交的公开事实，不再次构造 Provider 或执行工具；同 ID、不同载荷直接冲突。运行中、已中断等不确定状态在真正 resume 落地前安全阻断。

本切片同时提供纯 `Record → SessionView` Reducer、带版本/SHA-256/CAS 的 Snapshot 缓存，以及中文优先的 `session list/show/delete`。Snapshot 损坏或版本不兼容时会被丢弃并从事件全量重建，SQLite 事件仍是唯一事实源。

## 2. 数据与事务边界

- migration 002 新增 `commands`、`snapshots`、查询索引和 Run/Event 到 Command 的一致性触发器。
- Command 载荷先解析 JSON、重新规范编码，再计算 SHA-256；重复 ID 只有 kind 与摘要同时一致才视为幂等。
- 新 Session、Command、首个 Run 在同一事务登记；公开 Record 持久化 `command_id`。
- 第一条事实把 Command 从 `accepted` 推进为 `running`；Run 终态事件、Session/Run 状态、Command 终态和 `result_seq` 在同一追加事务提交。
- Provider 或本地 Runtime 尚未启动就失败时，Command 明确进入 `rejected`，不伪造终态事件或结果序号。

## 3. Reducer 与 Snapshot

- Reducer 不访问 SQL、时钟或外部系统，覆盖 run、完成消息、Todo、Approval、Tool、Usage、Warning 与终态。
- 未识别的未来事件仍推进 `last_seq`，但不会破坏旧版本投影。
- Snapshot 存储 schema 版本、through sequence、JSON 状态与 SHA-256；保存使用期望旧序号避免并发倒退。
- Snapshot 只缓存公开派生状态。哈希错误、JSON 损坏、身份/序号不一致或版本不兼容都会触发删除缓存并全量事件回放。

## 4. CLI 与隐私

新增命令：

```bash
mengdie exec --command-id ci-job-42 "检查项目"
mengdie session list [--all] [--json]
mengdie session show [--json] <session-id>
mengdie session delete --yes [--json] <session-id>
```

`session` 子命令只经过 Session Service 获取公开投影，不直接读 SQL；默认列表按当前项目过滤，`--all` 才跨项目。删除会级联移除本地 Command、Run、Event 与 Snapshot，并强制显式 `--yes`。

公开事件、JSONL、SessionView 与人类输出均不含完整任务。为校验 Command 幂等性，完整任务会作为私有 `commands.payload_json` 存入本地 SQLite；当前仅依赖平台目录与文件权限，尚未静态加密。API Key、环境变量值、Authorization Header、隐藏推理和可重放审批授权不进入账本。

## 5. 恢复边界

本切片不实现模型上下文恢复、`resume`、pending Approval 恢复、只读工具自动重试、写工具未知状态判断或 TUI。终态 Command 可以安全回放；非终态 Command 只解释当前状态并拒绝自动续跑。上述能力进入 P2-03B，TUI 仍在事实恢复闭环之后实现。

## 6. 验证证据

- 测试覆盖 Command 同载荷幂等、异载荷冲突、独立身份、终态原子推进、未启动拒绝、Reducer 全事件族、未知事件、Snapshot CAS/损坏回退、项目过滤、删除级联、CLI 隐私和终态回放不调用 Provider。
- `gofmt -l .` 无未格式化文件，`go vet ./...` 通过，`go test ./...` 与 `go test -race ./...` 均为 416 项 / 17 个包通过。
- `golangci-lint run --build-tags=liveprovider ./...` 为 0 issue，包含 errcheck；`govulncheck@v1.1.4 ./...` 未发现漏洞。
- Windows amd64、macOS amd64/arm64 和 Linux amd64 四目标 `CGO_ENABLED=0` 构建全部通过。
