# M0/M1 真实仓库评测方案

> 状态：评测协议草案；候选 manifest 校验、受限 GitHub 源码准备 API 与单任务本地基线诊断已实现，真实仓库 Agent runner 与任务集尚未完成。
> 适用范围：Coding Daily Set、Long-run Set，以及 M1 双平台真实任务出口。

## 目标

把 Agent 的真实仓库表现变成可复跑、可比较的证据。现有 `evals/coding/smoke.json` 和 M1 `m1-coding` 工作流运行的是仓库内 Go fixture；它们验证协议和受限读改测闭环，不属于外部真实仓库任务。M2 Chaos Set 验证中断和恢复边界，也不替代包含真实 Coding 目标的长任务评测。

本方案固定数据与记录协议。`PrepareRealRepositoryTask` 只为一个已校验任务准备固定 commit 的临时源码目录；`mengdie-eval repo baseline` 可显式运行一个公开 verifier 的本地未隔离基线诊断。两者都不是 Agent runner，也不产生正式真实仓库成绩。完整 runner 仍应作为单独切片评审，避免把任意 Git URL、工作目录或命令直接接入 fixture runner。

Agent、独立 verifier、执行环境与证据的具体边界见[真实仓库单任务执行边界](./REAL_REPOSITORY_EXECUTION_BOUNDARY.md)。该设计尚未实现；目前没有可计入 M1 出口的真实仓库运行成绩。

### 当前源码准备切片

- 只接受 `github.com` HTTPS 来源和清单中的完整 commit SHA；Git fetch 禁止凭据 helper、重定向、子模块、外部协议、模板钩子和 LFS smudge，并通过独立 Git 配置与环境变量运行。
- checkout 后核对 `HEAD` 与固定 SHA，并要求工作树干净；超时、取消或任一步失败都会清理临时目录。macOS/Unix 使用仅当前用户权限的目录模式，Windows 对目录设置仅当前用户 SID 的受保护 DACL。
- 该目录尚未提供 OS 级执行沙箱或网络隔离。取得源码不等于任务已运行；后续 Agent 执行、独立 verifier、权限边界和证据记录必须单独实现与评审。

## 评测集边界

### Coding Daily Set

- M0 至少 20 个来自真实公开仓库的独立任务。
- 每项记录仓库 URL、许可证、固定 commit SHA、任务来源和任务前基线。
- 至少 5 项有独立、可自动执行的验收器；其余可以由不知道模型身份的审阅者按书面标准判定。
- 覆盖 bug 修复、测试补充、局部重构和代码解释；明确每项任务是否允许改测试、依赖或多个文件。
- 初始任务集不复制进本仓库；记录公开源仓库与固定 revision，按其许可证和服务条款获取。

### Long-run Set

- 覆盖需要 20 次以上工具调用的真实目标，至少包含正常完成、用户取消/进程中断、恢复、上下文压缩和恢复后继续验证的场景。
- 记录任务目标、成功条件、必须保留的关键信息、允许副作用以及终态判定。
- 每个场景独立运行，不把 Chaos Set 的 subsystem 注入场景计作真实长任务成功。
- 对未知执行结果、重复副作用、恢复失败和遗漏任务目标分别计数，不将中断状态推断为成功。

### Memory Trust Set

继续使用 `evals/memory/trust-set-v1.json` 及其 runner；新增场景需记录来源、权威、作用域、有效期、冲突和删除预期。Memory Trust 指标单独报告，不与 Coding 成功率或 Long-run 完成率合并成单一分数。

## 任务定义记录

每个任务至少填写以下字段。字段是评测协议，不代表当前 JSON manifest 已支持这些属性。

| 字段 | 内容 |
|---|---|
| `task_id` | 稳定、不可复用的任务标识 |
| `source_url` / `source_commit` | 上游仓库与完整 commit SHA；禁止只记可变 branch/tag |
| `license` / `task_source` | 仓库许可证与问题、回归、维护者设计等任务来源 |
| `environment` | OS、架构、语言工具链及必要的预装依赖 |
| `prompt` | 给 Agent 的任务原文，不包含验收答案或隐藏测试细节 |
| `baseline` | 任务开始前的验证结果、工作树状态和必要文件哈希 |
| `acceptance` | 用户可见完成条件、允许修改范围、禁止改动范围 |
| `verifier` | 独立 argv 命令、版本、超时和成功判据；人工验收需有评分准则 |
| `risk_budget` | 允许的工具、副作用、审批和外部访问范围 |

任务审阅应排除依赖私有服务、未公开密钥、不可复现网络状态、含糊验收、必须知道维护者未公开意图、或在评测期间可能已被上游修复的题目。任务集发布前由人工复核来源和许可。

## 运行协议

1. 每个任务从干净的临时工作区开始，在相同固定 commit 上运行；不同任务、平台和 Agent 版本不共享工作树。
2. 运行前记录 OS/架构、MengDie 版本/commit、Provider 与模型标识、配置 profile、开始时间和初始 verifier 状态。API Key 只从宿主环境注入，不写入任务定义或结果。
3. 只开放该任务声明的工具和权限。网络默认关闭；若任务或 Provider 需要联网，区分 Provider 流量和 Agent 工具网络权限并记录目标。
4. Agent 结束后先记录终态与 diff 摘要，再由独立 verifier 对同一工作区运行。不得仅凭模型自述判定成功。
5. 核对 diff 白名单、测试/依赖改动、仓库外写入、命令越权和遗留进程。任何未知副作用均按失败或待调查记录，不按零处理。
6. 对照 CLI 基线时使用同一任务、相同初始 commit 和相同验收条件，并记录工具版本与 Provider；比较结果描述为观测结果，不宣传为普遍排名。

当前 fixture runner 仅复制仓库内 fixture 并执行声明的验证 argv。真实仓库运行器落地前，真实任务按人工审核的临时副本流程执行并保存下方记录；不要给当前 fixture manifest 填入远端 URL 来绕过路径边界。

## 指标与报告

每个任务分别记录：

- Agent 任务完成与独立 verifier 通过情况；
- 修改文件数、白名单外 diff、测试或依赖改动；
- 未授权副作用数、审批次数、拒绝次数和遗留子进程数；
- 工具调用数、人工介入次数、总耗时、输入/输出 token、费用（Provider 未提供时标记 unavailable）；
- 中断场景的恢复结果、重复副作用数、压缩前后关键要求保持情况；
- 失败类别与人工审阅结论。

至少按平台、Provider/模型和任务类别分别给出分子/分母，报告失败和未运行项。平台或 Provider 的样本不足时标记样本量，不合并掩盖差异。API Key、Authorization header、用户私有仓库路径、未授权代码片段和隐藏推理不得进入公开证据；公开报告优先使用汇总数据、任务 ID、版本号、脱敏日志和 diff 路径/哈希。

### 任务运行记录模板

```text
task_id:
source_url:
source_commit:
license:
task_source:
platform (OS/arch):
mengdie_commit/version:
provider/model:
started_at_utc:
verifier_before:
verifier_after:
agent_terminal_state:
accepted_changes:
out_of_scope_changes:
tool_calls:
human_interventions:
unauthorized_side_effects:
orphan_processes:
tokens_and_cost:
resume_or_compaction_result:
review_result_and_notes:
evidence_reference:
redaction_reviewed:
```

### M1 出口核对

按 [`phase-1/DETAILED_DESIGN.md`](../phase-1/DETAILED_DESIGN.md) 的要求，M1 仍需在 macOS 与 Windows 各完成至少 5 个真实仓库任务，至少 3 个有完整“读 → 改 → 测 → 修正”循环，且未授权写入/命令为 0、取消后无遗留子进程。还需满足 Provider 错误行为、连续 20 次跨平台 CI、质量门禁和安全出口记录。现有 5 个 fixture 双平台 10/10 只计入 fixture 预验收，不能计入上述外部真实仓库数量。

完成真实任务集定义和协议不等于 M0/M1 评测通过。只有保留任务版本、运行结果、失败项与复核证据后，才更新 README 的里程碑状态。
