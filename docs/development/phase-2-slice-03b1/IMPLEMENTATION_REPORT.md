# P2-03B1 私有上下文日志与安全 Session Resume 实施报告

> 状态：已实现，待 PR 审核  
> 日期：2026-08-10  
> 范围：私有模型边界、恢复分析器、同 Session 新 Run、`session resume`

## 1. 交付结论

本切片实现可用但保守的 Session Resume：命令只在私有上下文、公开事实、项目身份和工具状态能够共同证明安全时创建新 Run。恢复不会冒充旧 HTTP 流，也不会重新执行已完成工具；任何未知状态都以中文原因拒绝。

```bash
mengdie session resume [--json] [--message 文本] [--command-id ID] <session-id>
```

恢复命令沿用无头安全参数 `--allow-edit`、`--allow-command` 与 `--allow-env`。默认新指令要求先依据当前仓库状态重新验证；同恢复 Command ID、同目标和同消息只回放该 Run 已提交的公开事实，不再调用 Provider 或工具。

## 2. 私有上下文边界

- migration 003 新增 `context_messages`，以 Session ordinal 排序，绑定 Session/Run/Command，并校验角色、完整度、大小、SHA-256 和外键身份。
- Agent 在下一次模型调用前 store-first 写入新 user、assistant 与 tool 消息；写入失败立即终止 Run。
- assistant 与 read/state 工具结果保存完整模型可见边界；write/execute/network 结果只保存不含原始输出和环境值的恢复摘要。
- 私有消息不进入公开 Event、SessionView、JSONL 或日志；`session delete --yes` 级联删除。

## 3. 恢复门禁与事务

Resume Analyzer 联合纯 Reducer 视图与私有上下文链，检查：

- 当前项目身份与目标 Session 一致；
- 上下文 ordinal 连续、校验和正确、每个 Run 仅一个 user 起始边界；
- assistant/tool 私有边界与公开 `message.completed`/`tool.completed` 一一对应；
- Assistant 工具调用与同 Run 工具结果配对完整；
- 不存在 pending Approval 或 proposed/running 工具；
- 旧版无上下文日志的会话明确不可恢复。

分析通过后，`BeginResumeCommandRun` 在同一 SQLite 写事务重新检查 Session sequence 与 context ordinal，标记遗留 active Run/Command 为 interrupted，并在原 Session 创建独立恢复 Command 与新 Run。位置变化会产生乐观并发冲突，避免分析和登记之间的 TOCTOU。

## 4. 多 Run 投影与隐私

Tool/Approval 投影身份升级为 `(run_id, call_id)`，不同 Run 重用 Provider call ID 不会互相覆盖。Snapshot 仍只是公开投影缓存，SQLite Event 与私有上下文表分别承担公开事实和模型恢复事实。

JSON 恢复门禁只输出 `can_resume`、中文原因、序号和统计，不输出 History、Todo 内容、Command payload 或模型可见源码。SQLite 私有事实尚未静态加密，仍依赖平台数据目录权限和显式删除。

## 5. 明确未实现

- pending Approval 的重新展示、重新校验与重新签发 Capability；
- 执行中只读工具的自动重试；
- 写工具未知状态、Patch Journal、rewind；
- TUI、Artifact Store 和上下文压缩。

这些能力分别进入 P2-03B2、P2-04 及后续切片；本切片遇到对应状态只阻断，不宣称已经恢复。

## 6. 验证

测试覆盖上下文 round-trip/冲突/损坏/级联删除、Agent store-first 与副作用结果脱敏、恢复历史进入 Provider、公开/私有边界不一致、pending 工具阻断、分析位置并发冲突、多 Run 投影和恢复 Command 幂等回放。

- `go vet ./...` 无问题；`go test ./...` 与 `go test -race ./...` 均为 429 项 / 17 个包通过；
- `golangci-lint run --build-tags=liveprovider ./...` 为 0 issue，包含 errcheck；
- `govulncheck@v1.1.4 ./...` 未发现漏洞；Coding baseline 5/5 通过；
- Windows amd64、macOS amd64/arm64、Linux amd64 四目标 `CGO_ENABLED=0` 构建通过。
