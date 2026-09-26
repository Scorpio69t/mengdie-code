# 真实仓库任务候选集 v0.1

> 状态：待审核候选，不是已验收任务集。候选来自 [SWE-bench Multilingual](https://huggingface.co/datasets/SWE-bench/SWE-bench_Multilingual) 数据集记录。此处仅保存实例 ID、仓库标识和固定源码 commit，不复制数据集中的补丁、隐藏测试或完整任务文本。实例编号不保证等于 GitHub Issue 编号；下表只附已按公开交叉引用核验的来源。

## 候选记录

| task_id | 仓库 / 许可证 | 固定源码 commit | 数据集来源 | 候选目标 | 当前状态 |
|---|---|---|---|---|---|
| `gin-gonic__gin-1805` | [gin-gonic/gin](https://github.com/gin-gonic/gin)，MIT（已在固定 commit 核验） | `70a0aba3e423246be37462cfdaedd510c26c566e` | SWE-bench Multilingual instance `gin-gonic__gin-1805`；对应 [Gin Issue #1804](https://github.com/gin-gonic/gin/issues/1804) / [PR #1805](https://github.com/gin-gonic/gin/pull/1805) | StaticFS 处理 404 时的重复日志 | 来源、许可证、基线与 macOS 独立 verifier 已核验；Agent 未完成，Windows 与资格待审核 |
| `gin-gonic__gin-1957` | [gin-gonic/gin](https://github.com/gin-gonic/gin)，MIT（已在固定 commit 核验） | `09a3650c97ca7ef3a542d428a9fb2c8da8c18002` | SWE-bench Multilingual instance `gin-gonic__gin-1957`；对应 [Gin Issue #1956](https://github.com/gin-gonic/gin/issues/1956) / [PR #1957](https://github.com/gin-gonic/gin/pull/1957) | HTTP Header 参数绑定能力 | 来源、许可证与缺失 API 基线已核验；实现和 verifier 待审核 |
| `gin-gonic__gin-2121` | [gin-gonic/gin](https://github.com/gin-gonic/gin)，MIT（已在固定 commit 核验） | `2ee0e963942d91be7944dfcc07dc8c02a4a78566` | SWE-bench Multilingual instance `gin-gonic__gin-2121`；对应 [Gin PR #2121](https://github.com/gin-gonic/gin/pull/2121) | `DataFromReader` 收到 nil headers 时的崩溃 | 来源映射、许可证、基线复现和 macOS verifier 已核验；Windows 与正式资格待审核 |
| `gin-gonic__gin-2755` | [gin-gonic/gin](https://github.com/gin-gonic/gin)，MIT（已在固定 commit 核验） | `f2bbdfe9f26d84cb994f381050692a9e4553bf75` | SWE-bench Multilingual instance `gin-gonic__gin-2755`；对应 [Gin Issue #2690](https://github.com/gin-gonic/gin/issues/2690) / [PR #2755](https://github.com/gin-gonic/gin/pull/2755) | `CreateTestContext` 后调用 `HandleContext` 的 panic | 来源、许可证和基线已核验；Agent 未完成，独立 verifier 与资格待审核 |
| `gin-gonic__gin-3227` | [gin-gonic/gin](https://github.com/gin-gonic/gin)，MIT（已在固定 commit 核验） | `51aea73ba0f125f6cacc3b4b695efdf21d9c634f` | SWE-bench Multilingual instance `gin-gonic__gin-3227`；对应 [Gin Issue #2282](https://github.com/gin-gonic/gin/issues/2282) / [PR #3227](https://github.com/gin-gonic/gin/pull/3227) | `RedirectFixedPath` 对尾斜杠的行为 | 来源、许可证和 macOS 基线已核验；post-fix verifier 与资格待审核 |

## 资格审核状态

### 2026-09-26 来源与许可证核对

已通过官方数据集公开行读取各任务描述和 `base_commit`，并以 GitHub 公开 cross-reference 核对 Issue 与修复 PR。Gin 的 LICENSE 文件在五个固定 commit 上均为 MIT。对应关系为：实例 1805 → Issue #1804 / PR #1805；1957 → Issue #1956 / PR #1957；2121 → PR #2121；2755 → Issue #2690 / PR #2755；3227 → Issue #2282 / PR #3227。评测准备时不可直接用实例编号猜 Issue 号。

`gin-gonic__gin-2121` 已完成 macOS Agent 运行和独立 verifier；`gin-gonic__gin-1805` 已完成 macOS 基线复现与补丁独立验证，但 Agent run 未完成；`gin-gonic__gin-2755` 与 `gin-gonic__gin-3227` 已完成 macOS 基线复现，但没有完成 Agent 与 post-fix verifier；`gin-gonic__gin-1957` 已确认 API 在基线中不存在，尚未评估实现范围和 verifier。五项仍属于同一 Go 项目的 pilot 候选，不构成正式 M0/M1 任务集。

### Gin `gin-gonic__gin-1805` 核验记录

- SWE-bench instance 对应 [Gin Issue #1804](https://github.com/gin-gonic/gin/issues/1804)，由 [Gin PR #1805](https://github.com/gin-gonic/gin/pull/1805) 修复；PR base SHA 与本表固定 commit 一致。固定 commit 的 `LICENSE` 为 MIT。
- 2026-09-26 在干净基线按 issue 场景请求缺失的 StaticFS 文件，响应为 404 且 logger 输出两行，复现成功。
- Agent 提交了 `routergroup.go` 与 `routes_test.go` 两文件改动，但运行以 `max_turns` 结束，未调用 shell 测试，按 Agent 评测规则不计通过。随后独立 `go test ./...` 全包通过；细节见 [M1 出口评测记录](../../docs/development/phase-1-slice-12/M1_EXIT_EVALUATION.md#10-2026-09-26-gin-1805-探索性运行)。
- Windows verifier 和正式任务资格仍待审核。

### Gin `gin-gonic__gin-2755` 基线记录

- 2026-09-26 在固定 commit `f2bbdfe9f26d84cb994f381050692a9e4553bf75` 按 [Gin Issue #2690](https://github.com/gin-gonic/gin/issues/2690) 的公开步骤复现：`CreateTestContext` 后对带 `:name` 参数的路由调用 `HandleContext`，触发 `slice bounds out of range` panic。
- Agent 首次探索没有修改文件；它尝试以未授权的 shell 命令 `ls tree.go tree_test.go 2>&1` 探查文件，之后因达到 `max_turns` 结束。故本次没有 Agent 补丁或 post-fix verifier，不能计为任务通过。
- 后续若重试，应在提示中明确用 `list_files` / `read_file` 查看文件，并只允许原样 `go test ./...` 执行验证。

### Gin `gin-gonic__gin-3227` 基线记录

- 2026-09-26 在固定 commit `51aea73ba0f125f6cacc3b4b695efdf21d9c634f` 按 [Gin Issue #2282](https://github.com/gin-gonic/gin/issues/2282) 的公开步骤复现：`RedirectTrailingSlash=false`、`RedirectFixedPath=true` 时请求 `/ping/`，响应为 301 并跳转 `/ping`；issue 预期为 404。
- 该基线成立，固定版本 MIT 许可已核验。尚未运行 Agent、补丁或 post-fix verifier；本候选不能计通过。

### Gin `gin-gonic__gin-1957` 基线记录

- 2026-09-26 核对 [Gin Issue #1956](https://github.com/gin-gonic/gin/issues/1956) / [PR #1957](https://github.com/gin-gonic/gin/pull/1957) 与固定 commit `09a3650c97ca7ef3a542d428a9fb2c8da8c18002`，该 revision 的 `LICENSE` 为 MIT。
- Issue 目标是新增 HTTP Header 到结构体的绑定能力。固定 commit 的 `context.go` 和 `binding/` 中没有 `ShouldBindHeader` / `HeaderBinding` API；这属于新功能，不是现有行为 bug。
- 尚未运行 Agent 或实现后的独立 verifier；需先定义 Header 标签、类型转换、校验失败和未知 Header 的验收范围。

### Gin `gin-gonic__gin-2121` 核验记录

- 2026-09-26 核对 SWE-bench Multilingual instance 与上游 [Gin PR #2121](https://github.com/gin-gonic/gin/pull/2121)，标题为 `[FIX] allow empty headers on DataFromReader`。PR 的 base SHA 与本表固定 commit 完全一致；PR 于 2019-11-25 合并。
- 固定 commit 中的 `LICENSE` 为 MIT。
- 在干净基线手工调用 `DataFromReader(..., nil)` 可复现 nil map panic。未向 Agent 提供 SWE-bench 参考补丁或隐藏测试。
- macOS arm64、Go 1.27.0 上 Agent 完成读、改、测，独立 `go test ./...` 通过。运行证据见 [M1 出口评测记录](../../docs/development/phase-1-slice-12/M1_EXIT_EVALUATION.md#9-2026-09-26-gin-修正提示后的探索性重跑)。
- Windows verifier、其他平台行为和正式任务 prompt/文件范围复核尚未完成；本候选不计 M1 正式通过数。

这五项只构成同一 Go 项目中的候选 pilot，不能代表多仓库、多任务类别的 M0 Coding Daily Set，更不能计入 M1 的 macOS/Windows 真实任务完成数。每项进入正式任务集前，仍需：

1. 在所列 commit 检查许可证文件、上游问题映射及任务来源；记录检查日期。不能只依赖数据集实例编号推断 Issue 编号。
2. 从干净 checkout 验证题目在基线上的现象，并记录初始工作树状态。
3. 编写不含验收答案的任务 prompt，审定允许修改的文件范围和风险预算。
4. 独立复核 verifier 的命令、版本、超时、平台支持及基线结果；验证器和任何参考补丁不得放进 Agent 可读工作区。
5. 评估依赖安装和构建成本；不因上游 benchmark 提供了容器就推定它适合 macOS 与 Windows 的 M1 出口。

任务通过审核后，复制[评测方案中的任务定义记录](../../docs/design/phase-0/REAL_REPOSITORY_EVALUATION.md#任务定义记录)和[运行记录模板](../../docs/design/phase-0/REAL_REPOSITORY_EVALUATION.md#任务运行记录模板)，再将此处状态改为已审核并链接审核记录。未满足上述条件的候选不得计入分子或分母。

## 数据集使用边界

SWE-bench Multilingual 是外部研究基准；其数据集记录可用于发现候选来源，但其容器、测试补丁和任务划分不自动成为 MengDie Code 的运行器或验收依据。遵守数据集及各上游仓库的许可与条款；不要将数据集中的参考 patch、隐藏测试或未经审核的完整记录复制到本仓库。若将来复用其 verifier，先独立审查脚本、依赖和执行隔离，并把 verifier 放到 Agent 无法读取的隔离区。
