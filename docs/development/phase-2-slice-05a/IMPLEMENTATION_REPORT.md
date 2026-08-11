# P2-05A Artifact Store 与大上下文离线化实施报告

> 状态：已实现，待 PR 审核  
> 日期：2026-08-11  
> Beads：`mengdie-pdy`

## 用户痛点与切片边界

此前私有模型上下文只能以内联 JSON 写入 SQLite；单条消息接近上限时既放大数据库，又缺少独立的文件完整性和生命周期边界。本切片属于 Harness 的上下文与持久化层，只完成“原始大上下文可离线、可校验、可随 Session 清理”的最小闭环。模型滚动摘要、token 预算、工具原始无限输出、Patch Journal 和跨项目复用不在本切片实现。

## 写入与恢复契约

- 单条序列化上下文不超过 64 KiB 时继续内联，避免为小消息制造文件开销；超过阈值且不超过 16 MiB 时写入私有 Artifact；
- 文件名只由内部 `art_<sha256>` ID 生成，数据库仅保存受控相对路径、MIME、敏感级别、大小与 SHA-256；
- 写路径固定为同目录临时文件 → `fsync` → 原子 rename → SQLite 事务同时登记 Artifact 与上下文引用；数据库登记或乐观序号检查失败时删除文件。进程在 rename 后崩溃留下的孤儿由启动扫描清理；扫描只删除超过一小时安全宽限期的内部命名文件，避免另一个刚启动进程误删仍在登记窗口内的文件；
- 恢复读取重新检查 Artifact 根目录、相对路径、普通文件类型、大小和 SHA-256。缺失、篡改、路径逃逸或索引缺口全部归类为上下文损坏，Resume fail-closed，不使用 SQLite 中的小型占位文本冒充原始上下文；
- Artifact 始终标为 private，不经过 Public Event Projector、EventBus、TUI 或 JSONL。

## 配额、权限与删除

默认每个 Session 128 MiB、全局 512 MiB；配额在写入索引的同一事务内统计，超限拒绝新 Artifact，不删除现有证据。单文件硬上限为 16 MiB。测试可以注入更小配额验证失败路径。

Unix 下 Artifact 目录复验为 `0700`、文件为 `0600`；Windows 继续使用受控用户数据目录和现有 reparse-point 拒绝规则。每次解析相对路径都会重新确认 Artifact 根没有被替换为 symlink/reparse point。

`session delete --yes` 在事务内取得所属文件列表并删除 Session 数据，提交后清理文件。清理失败不会伪装成 Session 未删除，而是返回包含内部相对残留路径的 `ArtifactCleanupError`，便于人工处理；下次启动也会回收已经失去数据库索引的内部命名文件。

## Schema

Migration 004 新增 `artifacts` 表、Session/Run 外键、配额索引和身份触发器，并为 `context_messages` 增加可空的 `artifact_id`。删除 Session 时上下文和 Artifact 一起级联；独立删除 Artifact 会把引用置空，使后续上下文读取明确失败，而不是读到错误内容。

## 验证

- 覆盖内联/离线边界、完整往返、权限、配额、乐观冲突清理、文件缺失、内容篡改、路径逃逸、启动孤儿清理、Session 删除和残留报告；
- `go test ./internal/session`：93 项通过；
- `gofmt -l .`、`go vet ./...`、`go test ./...`：通过，482 项 / 18 个包；`go test -race ./...` 同样通过；
- `golangci-lint run --build-tags=liveprovider ./...`：0 issue；`govulncheck@v1.1.4 ./...`：未发现漏洞；
- Coding baseline：5/5；live-provider 离线 Harness：3/3；
- `CGO_ENABLED=0` 的 Windows amd64、macOS amd64/arm64、Linux amd64 构建通过。
