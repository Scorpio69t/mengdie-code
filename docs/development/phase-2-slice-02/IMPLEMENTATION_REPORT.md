# P2-02 SQLite EventStore 实施报告

> 状态：已实现，待 PR 审核
> 日期：2026-08-06
> 范围：SQLite 基础、migration、EventStore 最小闭环

## 1. 交付结论

本切片把 M1 的关键公开事件从“只在进程内经过”提升为本机 SQLite 中的可排序事实，并把输出链固定为 `EventStore commit → renderer`。Renderer 失败时已提交事实仍可读取；存储失败时事件不会被输出成已经发生。

本切片不实现历史列表、resume、Command Ledger、Snapshot、Artifact Store、Patch Journal 或 TUI。当前每个 Run 使用同一个 ID 创建一个过渡期 Session，先为 P2-03 提供真实、受测的事实源。

## 2. 数据与事务边界

- `internal/session.Record` 定义独立的 `session_seq`、`run_seq`、schema 版本、visibility、JSON payload 与时间；单条 payload 上限 1 MiB，单批最多 256 条，字符串标识均有边界。
- `EventStore.Append` 使用 `expectedSeq` 乐观并发控制，在一个事务内占用 Session 序号、写入事件并更新 Session/Run 索引；冲突返回 expected/actual，不静默覆盖。
- `Load(sessionID, afterSeq, limit)` 严格按 `session_seq` 读取，并重新校验时间、JSON 和领域约束；返回 payload 为防御性副本。
- Run 终态只能提交一次，终态后的追加和 Run 序号倒退均 fail-closed。
- `run.started`、`message.completed`、warning、tool/approval 完成边界和 Run 终态会持久化；高频 `message.delta` 只转发，不落库。

## 3. Schema 与迁移

schema v1 包含 `sessions`、`runs`、`events` 和 `schema_migrations`：

- migration SQL 编译进二进制，不依赖运行目录；
- 每个 migration 独立事务执行并记录 SHA-256；
- 已应用文件的版本、名称或内容变化均返回 drift 错误；
- 数据库版本高于当前二进制时拒绝打开，避免旧程序误写新 schema；
- 失败 migration 不留下半张表。

连接固定为 WAL、foreign keys、`synchronous=FULL`、`BEGIN IMMEDIATE` 和 5 秒默认 busy timeout。SQLite 连接池限制为一个连接，以维持当前单进程单写者模型；busy、locked、full、重复事件和序号冲突都有稳定错误分类。

## 4. 数据目录与隐私

默认目录：

| 平台 | 路径 |
|---|---|
| macOS | `~/Library/Application Support/MengDie Code/` |
| Windows | `%LOCALAPPDATA%\MengDie Code\` |
| Linux | `$XDG_STATE_HOME/mengdie/`，否则 `~/.local/state/mengdie/` |

`MENGDIE_DATA_DIR` 可覆盖。数据根不能是文件系统根、项目目录、网络共享、OneDrive/iCloud 同步目录或 symlink/reparse point。Unix 目录和数据库分别复验为 `0700` 与 `0600`；Windows 使用当前用户本地应用目录和默认 ACL。

适配器只持久化已有的公开事件投影，不写入完整用户任务、API Key、环境变量值、Authorization Header、隐藏推理或可重放审批授权。集成测试同时扫描 SQLite 文件字节，验证注入的私有任务和密钥未出现。

## 5. 依赖 spike

采用 `modernc.org/sqlite` v1.56.0：

- 纯 Go、`database/sql`、BSD-3-Clause，可保持现有四目标 `CGO_ENABLED=0` 分发；
- 支持本切片验证过的 `_busy_timeout`、`_foreign_keys`、`_journal_mode`、`_synchronous` 与 `_txlock` DSN 配置；
- 直接版本固定，传递依赖由 `go.sum` 锁定，其中 `modernc.org/libc` 为 v1.74.4；
- driver 只存在于 `internal/session`，核心接口不暴露第三方类型。

Windows amd64 stripped CLI 从引入前 7,857,152 B 增至 11,836,928 B，增加 3,979,776 B（50.7%）。这是本地可靠事实源的明确分发成本；当前仍低于 12 MiB，后续发布持续记录趋势。

本切片四目标 stripped 构建结果：

| 目标 | 字节 |
|---|---:|
| Windows amd64 | 11,836,928 |
| macOS amd64 | 11,709,152 |
| macOS arm64 | 11,143,426 |
| Linux amd64 | 11,493,538 |

## 6. 验证证据

- 单元/集成测试覆盖 schema/PRAGMA、迁移漂移与回滚、追加/读取、序号冲突、重复事件、busy 有界等待、损坏 payload、终态保护、数据目录平台规则和权限。
- App 集成测试覆盖可重建边界落库、delta 不落库、Renderer 失败保留事实、Store 失败不广播，以及任务/密钥禁存。
- `go fmt ./...`、`go vet ./...`、`go test ./...` 与 `go test -race ./...` 全部通过（404 项 / 17 个包）。
- `golangci-lint run --build-tags=liveprovider ./...` 通过，包含 errcheck 且为 0 issue；`govulncheck@v1.1.4 ./...` 未发现漏洞。
- `go-licenses@v1.6.0 report ./...` 完成依赖清单；旧版识别器对 `modernc.org/mathutil` 报 Unknown，复核模块自带 `LICENSE` 为 BSD-3-Clause，其余 SQLite 可达依赖为 MIT/BSD 兼容许可证。
- Windows amd64、macOS amd64/arm64 和 Linux amd64 四目标 `CGO_ENABLED=0` 构建通过，体积见上一节。

## 7. 下一切片入口

P2-03 将在该事实源上加入稳定 Session/Command 身份、纯 Reducer、恢复状态机和 CLI 历史/resume。TUI 仍按既定顺序位于 P2-04：它消费 EventStore 回放和提交后的实时事件，不成为第二事实源。
