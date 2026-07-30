# MengDie Code 评测

第一批评测固定“开始任务前应失败”的仓库状态，为后续 Agent 版本提供可比较的真实起点。

运行全部 baseline：

```bash
go run ./cmd/mengdie-eval --manifest evals/coding/smoke.json --pretty
```

成功表示五个 fixture 都能隔离复制、执行，并产生 manifest 声明的预期退出码；不表示 Agent 已经能够修复这些任务。

## Manifest 约束

- `schema_version` 当前必须为 `1`。
- `fixture_root` 相对 manifest 所在目录解析。
- 每个 `fixture` 必须位于 `fixture_root` 内，禁止 `..` 逃逸。
- `verify.command` 是 argv 数组，不经过 shell 插值。
- baseline 输出是稳定 JSON，可由 CI 或后续对比工具消费。

