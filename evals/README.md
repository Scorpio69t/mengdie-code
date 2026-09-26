# MengDie Code 评测

第一批评测固定“开始任务前应失败”的仓库状态，为后续 Agent 版本提供可比较的真实起点。

运行全部 baseline：

```bash
go run ./cmd/mengdie-eval --manifest evals/coding/smoke.json --pretty
```

成功表示五个 fixture 都能隔离复制、执行，并产生 manifest 声明的预期退出码；不表示 Agent 已经能够修复这些任务。

此 CLI harness 目前只支持仓库内 fixture，不负责执行外部真实仓库。内部已有受限的源码准备 API，可按校验后的单个任务固定 SHA 克隆公开 GitHub 来源到临时目录；它不运行项目代码、Agent 或 verifier，也不提供 OS 执行沙箱。外部任务的来源固定、隔离运行、结果记录与 M1 证据要求见[真实仓库评测方案](../docs/design/phase-0/REAL_REPOSITORY_EVALUATION.md)；在完整 runner 落地前，不要把 fixture 结果写成真实仓库任务成绩。

真实仓库候选的机器可校验任务定义示例位于 `coding/real-repository-v0.1.json`。可运行：

```bash
go run ./cmd/mengdie-eval repo validate --manifest evals/coding/real-repository-v0.1.json --pretty
```

`repo validate` 只检查 HTTPS 来源、固定完整 commit、许可证、argv verifier、精确允许修改路径和风险预算，并输出省略任务 prompt 与 verifier 参数的 JSON 摘要。它不会访问网络、克隆仓库或执行清单中的命令；通过校验不代表候选任务已审核或评测通过。GitHub 源码准备 API 尚未接入 CLI，Agent 与独立 verifier 的隔离执行仍需单独实现和审查。

已整理一组待审核的 [真实仓库任务候选](coding/real-repository-candidates-v0.1.md)。候选不属于正式评测集，也没有运行成绩；通过来源、许可、基线、verifier 和平台审核后才可计入评测。

## Manifest 约束

- `schema_version` 当前必须为 `1`。
- `fixture_root` 相对 manifest 所在目录解析。
- 每个 `fixture` 必须位于 `fixture_root` 内，禁止 `..` 逃逸。
- `verify.command` 是 argv 数组，不经过 shell 插值。
- `acceptance.allowed_changes` 使用相对工作区的 `/` 路径；真实 Agent 预验收只接受白名单内 diff，修改测试、依赖文件或创建未声明文件都会失败。
- baseline 输出是稳定 JSON，可由 CI 或后续对比工具消费。
