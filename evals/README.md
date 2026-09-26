# MengDie Code 评测

第一批评测固定“开始任务前应失败”的仓库状态，为后续 Agent 版本提供可比较的真实起点。

运行全部 baseline：

```bash
go run ./cmd/mengdie-eval --manifest evals/coding/smoke.json --pretty
```

成功表示五个 fixture 都能隔离复制、执行，并产生 manifest 声明的预期退出码；不表示 Agent 已经能够修复这些任务。

原有 `baseline` CLI 只评测仓库内 fixture。`repo baseline` 可对一个公开仓库候选运行本地基线诊断；`repo run` 可再执行一次受预算约束的 Agent、本地 diff 核对与独立终态 verifier。两者均为 **`local_unisolated` 本地诊断**，没有 OS 文件或网络沙箱；执行中的项目代码拥有当前账户的宿主权限，不构成 M1 正式成绩。外部任务的来源和正式证据要求见[真实仓库评测方案](../docs/design/phase-0/REAL_REPOSITORY_EVALUATION.md)与[执行边界](../docs/design/phase-0/REAL_REPOSITORY_EXECUTION_BOUNDARY.md)。

真实仓库候选的机器可校验任务定义示例位于 `coding/real-repository-v0.1.json`。可运行：

```bash
go run ./cmd/mengdie-eval repo validate --manifest evals/coding/real-repository-v0.1.json --pretty
```

`repo validate` 只检查 HTTPS 来源、固定完整 commit、许可证、argv verifier、精确允许修改路径和风险预算，并输出省略任务 prompt 与 verifier 参数的 JSON 摘要。它不会访问网络、克隆仓库或执行清单中的命令；通过校验不代表候选任务已审核或评测通过。

审核源码、许可证和 verifier 后，操作者可显式执行单任务诊断（将二进制路径替换为本机 `go` 的**绝对路径**）：

```bash
go run ./cmd/mengdie-eval repo baseline \
  --manifest evals/coding/real-repository-v0.1.json \
  --task gin-gonic__gin-2121 \
  --verifier-bin /absolute/path/to/go \
  --allow-unisolated --pretty
```

该命令通过 GitHub HTTPS 获取固定 commit，在独立临时工作区直接执行 manifest 的 argv verifier；二进制必须由操作者指定且位于工作区外。verifier 的 HOME、临时目录和 Go 缓存均位于临时根目录，Go 模块代理关闭，不继承 Provider Key 或代理环境变量。stdout/stderr 各最多保留 32 KiB 用于哈希，超额取消进程树；公开 JSON 只含运行 ID、manifest/verifier 哈希、状态、退出码、耗时、截断标记和输出前缀哈希，不含原始输出，原始输出也不会被此命令保存。退出码 0 仅表示基线实际退出码与 manifest 预期一致；其他情况返回非零。`baseline_matched` 只校验退出码，不证明失败原因正确，也不代表 Agent 完成任务。依赖未预装或缺少 vendor 时，离线 verifier 可能因缺依赖而失败；此命令不会自动下载依赖，该候选任务需进一步审核，不能仅凭退出码判定其基线有效。

完成同样的任务审核并配置用户级 Provider profile 后，可运行本地 Agent 诊断：

```bash
go run ./cmd/mengdie-eval repo run \
  --manifest evals/coding/real-repository-v0.1.json \
  --task gin-gonic__gin-2121 \
  --verifier-bin /absolute/path/to/go \
  --allow-unisolated --allow-agent-edit \
  --agent-command 'go test' --pretty
```

`--allow-agent-edit` 与每个 `--agent-command` 都是操作者本次显式授权；manifest 的 `risk_budget` 只给出上限，不能自行放权。Agent 的文件工具在授权前核对精确可写路径，工具和命令调用在准备前原子计数。基线、Agent 和终态 verifier 分别使用固定 SHA 的独立副本；只有白名单内、文件类型和模式未变化的普通文件变更会被重建到终态副本。结果中 `diagnostic_passed` 只表示这些本地检查通过；仓库外副作用、直接网络访问、逃逸进程和宿主凭据无法由同账户诊断证明为零。报告只保留路径与哈希、次数和状态，不保存原始输出或代码片段。运行期仍关闭 Go 模块下载，缺少已安装依赖的候选会停在基线。不要把此命令的 JSON 当作正式 M1 成绩。

已整理一组待审核的 [真实仓库任务候选](coding/real-repository-candidates-v0.1.md)。候选不属于正式评测集；通过来源、许可、基线、verifier、平台和正式执行环境审核后才可计入评测。

## Manifest 约束

- `schema_version` 当前必须为 `1`。
- `fixture_root` 相对 manifest 所在目录解析。
- 每个 `fixture` 必须位于 `fixture_root` 内，禁止 `..` 逃逸。
- `verify.command` 是 argv 数组，不经过 shell 插值。
- `acceptance.allowed_changes` 使用相对工作区的 `/` 路径；真实 Agent 预验收只接受白名单内 diff，修改测试、依赖文件或创建未声明文件都会失败。
- baseline 输出是稳定 JSON，可由 CI 或后续对比工具消费。
