# P1-12：双平台真实 Provider Coding 预验收

## 1. 用户痛点与所属层次

单元测试、fake Provider 和交叉编译只能证明组件按预期工作，不能证明真实模型能在 macOS 与 Windows 上稳定完成 Coding 闭环。本切片属于 Harness 评测层：建立可重复、可审计的真实 Provider 预验收入口，不增加 Agent 自治能力，也不把仓库内 fixture 冒充外部真实仓库使用记录。

本轮最小闭环是：

```text
隔离复制任务 → 真实 Provider → read_file → edit/write → go test
           → 独立 verifier 再验证 → 输出工作流证据
```

## 2. 触发方式

GitHub Actions 的 `Provider 实机 Smoke` 仅支持 `workflow_dispatch`。选择：

- `provider`：`deepseek`、`kimi-code` 或 `kimi-platform`；
- `suite`：`readonly` 或 `m1-coding`。

`m1-coding` 在 `macos-latest` 和 `windows-latest` 分别执行 `evals/coding/smoke.json` 的 5 个任务。工作流绑定 `provider-smoke` Environment；建议配置 required reviewers，并在 Environment 中保存对应 Provider Secret。

该套件包含付费网络请求，不在普通 push/PR CI 中自动执行，也不允许同一 Provider 与套件并发运行。

Kimi Code 会员与 Kimi 开放平台使用不同的 Environment Secret、端点和额度：前者使用 `KIMI_CODE_API_KEY`，后者使用 `MOONSHOT_API_KEY`。工作流不自动猜测密钥类型，也不会在认证失败后把同一密钥转发到另一套平台。

## 3. 上下文、权限与事实边界

每个任务使用独立临时目录，只复制公开 fixture。注入模型的上下文来自任务 prompt、当前临时仓库、工具 schema 和现有 M1 系统规则；测试不会把其他任务输出或历史对话带入下一项。

权限固定为：

- 允许项目内只读工具；
- 允许本次 run 的 edit/write；
- 只允许 `go test` 命令前缀；
- manifest 为每项任务声明唯一允许变化的实现文件；测试、依赖文件和未声明新文件保持哈希不变；
- 不允许其他 Shell、项目外文件、网络工具或交互审批；
- Provider Key 只供宿主 Provider Client 使用，不传给独立 verifier，Shell 仍按产品环境过滤规则执行。

M1 仍然没有 EventStore、Artifact Store 或 session resume。工作流日志与 GitHub run URL 是本切片的外部验收证据，不冒充产品内持久会话。

## 4. 判定规则

每个任务必须同时满足：

1. CLI 以成功退出码结束，且没有 Policy 拒绝；
2. JSON Lines 中存在成功的 `read_file`、`edit_file`/`write_file`、`shell` 和 `run.completed`；
3. 不出现 `approval.needed`；
4. stdout/stderr 不包含 Provider Key；
5. 前后文件哈希证明所有 diff 都在 `acceptance.allowed_changes` 白名单内，且至少一个白名单文件实际变化；
6. Agent 结束后，独立 argv verifier 在同一临时工作区再次运行 manifest 命令并成功。

模型声称“测试通过”不是证据；一次 shell 成功也不能替代独立 verifier；修改测试来伪造通过会被哈希门禁拒绝。任一任务失败都会让对应平台 Job 失败，不把未知或部分成功写成通过。

## 5. 恢复、清理与限制

- 任务失败后保留 GitHub 日志，但临时工作区由 Go 测试框架清理；不会写回源码仓库。
- Job 超时 60 分钟，Go 测试总超时 50 分钟；每个 manifest verifier 使用自己的有界超时。
- 工作流不自动重试付费任务。需要重跑时由维护者检查失败类别后手动触发。
- 当前 5 个任务都是仓库内小型 Go fixture；它们只证明有界的真实 Provider 读改测闭环，不代表外部真实仓库任务、Coding Daily Set、Long-run Set 或 Memory Trust Set 已完成。
- 完整 TUI、EventStore、resume、rewind 和记忆仍属于后续里程碑。

## 6. M1 计入规则

只有同一审核提交上的 macOS 与 Windows `m1-coding` Job 均成功，才能记录“各平台 5 个真实 Provider fixture 任务”。PR 编译通过、只读 smoke 通过或验收入口合并都不能替代这次真实运行。这项记录仍不能替代第一阶段详细设计要求的外部真实仓库任务记录。

运行后应在 P1-12 Beads 记录：commit、Provider、两个 Job URL、任务通过数、未授权副作用数和是否发现密钥泄漏。M1 仍需同时满足第一阶段详细设计中的外部真实仓库任务、连续 20 次 main CI、安全专项与其余出口条件。

## 7. 已记录的真实运行

- 审核提交：`535c10e2e9df4742dd55758869b9eecdb2106543`；
- DeepSeek `readonly`：[31078797034](https://github.com/Scorpio69t/mengdie-code/actions/runs/31078797034)，macOS/Windows 均通过；
- DeepSeek `m1-coding`：[31079053820](https://github.com/Scorpio69t/mengdie-code/actions/runs/31079053820)，macOS 5/5、Windows 5/5；
- 未授权副作用：0；白名单外 diff：0；发现密钥泄漏：否；
- Kimi 首次 `readonly`：[31078807523](https://github.com/Scorpio69t/mengdie-code/actions/runs/31078807523)，双平台均返回 401。该失败证明旧样例混用了 Kimi Code 会员 Key 与开放平台端点，不能计为 Kimi Provider 通过；后续按显式双 profile 修复并重新验收。

## 8. 2026-09-25 本地补充运行（探索性，不计入 M1）

本轮在 macOS arm64、MengDie `85516cb402c2ee729c91faa60546a9a71a19c9d6` 上使用 SiliconFlow OpenAI-compatible endpoint 与 `deepseek-ai/DeepSeek-V4-Flash`。密钥由宿主环境提供，没有写入仓库、配置文件或本报告。

- 本地 Provider coding fixture：5/5 任务通过，合计约 99.75 秒。此结果是仓库内 fixture 预验收，不计为外部仓库任务。
- 外部仓库探索任务：Gin `gin-gonic__gin-2121`，固定源码 commit `2ee0e963942d91be7944dfcc07dc8c02a4a78566`；以候选集中的 nil headers panic 为目标，未使用数据集参考补丁或隐藏测试。2026-09-26 已核对对应 Gin PR #2121、base SHA 与该 revision 的 MIT 许可证；跨平台验证和正式任务资格仍待审核，故本次只作探索性记录。
- Agent 在隔离副本中修改 `render/reader.go`，为 nil `Headers` 初始化 map，并在 `context_test.go` 增加 `DataFromReader(..., nil)` 回归测试。Diff 仅含这两个预期文件，`git diff --check` 通过。
- 首次 Agent 运行终态为策略拒绝：事后从本地会话保存的工具调用参数确认，两次 shell 命令分别是 `go test ./... 2>&1 | tail -40` 和 `go test ./... 2>&1`。两条命令都带管道，触发策略对 shell 控制符的拒绝规则；CLI 返回非零。没有审批请求，事件日志未发现密钥泄漏。
- 随后在同一隔离副本独立运行 `go test ./...`，全部 Gin 包通过。该独立结果证明补丁当前可通过全量测试，但不改写 Agent 运行失败判定。
- Provider usage 共 13 次请求，70,626 输入 token、2,405 输出 token（合计 73,031）；运行时价格表没有该模型价格，费用状态为 unavailable。

此次是任务提示未明确禁止管道导致的评测失败；`go test` allowlist 本身工作正常。下节记录将 shell 命令约束写明后的干净副本重跑。候选来源、Issue 映射、许可证与 verifier 仍需审核，本探索任务不加入正式任务集分子或分母。

## 9. 2026-09-26 Gin 修正提示后的探索性重跑

在新的干净副本上从同一固定 commit 重跑，任务提示明确要求 shell 命令只能原样运行 `go test ./...`，禁止管道、重定向及复合命令。运行环境为 macOS arm64、MengDie `85516cb402c2ee729c91faa60546a9a71a19c9d6`、SiliconFlow `deepseek-ai/DeepSeek-V4-Flash`。

- Agent 退出码 0，耗时 43.16 秒；成功读取、编辑并通过一次 shell 测试调用。会话参数确认实际命令为 `go test ./...`，无审批请求。
- 独立 verifier 再次执行 `go test ./...`，Gin 全部包通过。Diff 只有 `render/reader.go` 与 `context_test.go`，且 `git diff --check` 通过。
- 未发现白名单外改动或密钥泄漏；临时 Provider 配置已从评测副本删除。
- Provider usage 为 9 次请求，30,109 输入 token、1,128 输出 token（合计 31,237）；运行时价格表没有该模型价格，费用状态为 unavailable。

这次证明在提示精确约束 shell 形式时，Agent 可完成该单平台外部仓库读改测闭环。该候选的 PR 来源、base commit 与许可证已核验，但 Windows verifier 和正式任务资格仍未完成，所以只作探索性结果，不计入 M1 正式任务通过数。

## 10. 2026-09-26 Gin 1805 探索性运行

候选 `gin-gonic__gin-1805` 对应 Gin Issue #1804 / PR #1805，固定 commit 为 `70a0aba3e423246be37462cfdaedd510c26c566e`，该 revision 的 `LICENSE` 为 MIT。运行环境为 macOS arm64、MengDie `85516cb402c2ee729c91faa60546a9a71a19c9d6`、SiliconFlow `deepseek-ai/DeepSeek-V4-Flash`。

- 干净基线复现成功：向 `StaticFS("/")` 请求不存在文件，HTTP 返回 404，默认 Logger 输出两行。
- Agent 改动仅涉及 `routergroup.go` 与 `routes_test.go`，新增单日志回归测试；Diff 检查通过。Agent run 因达到 `max_turns` 以非零退出，没有运行 shell 测试，因此按 Agent 评测规则 **未通过**。
- 独立 verifier 随后运行 `go test ./...`，Gin 所有包通过。没有审批请求，未发现密钥泄漏。
- Provider usage 为 12 次请求，375,691 输入 token、36,870 输出 token（合计 412,561）；该模型费用在运行时不可用。

代码补丁由独立 verifier 验证通过，但这次 Agent 运行没有完成读改测闭环，不计入 M1 通过数。该候选还需 Windows verifier 与正式任务资格审核。

## 11. 2026-09-26 Gin 2755 基线与 Agent 探索

候选 `gin-gonic__gin-2755` 对应 Gin Issue #2690 / PR #2755，固定 commit 为 `f2bbdfe9f26d84cb994f381050692a9e4553bf75`，该 revision 的 `LICENSE` 为 MIT。运行环境为 macOS arm64、MengDie `85516cb402c2ee729c91faa60546a9a71a19c9d6`、SiliconFlow `deepseek-ai/DeepSeek-V4-Flash`。

- 按公开 issue 复现步骤运行，`CreateTestContext` 后对带 path param 的路由调用 `HandleContext`，固定基线触发 slice bounds panic。
- Agent 没有修改文件。它尝试以 `ls tree.go tree_test.go 2>&1` 探查文件，该 shell 调用被 allowlist 拒绝，随后运行达到 `max_turns`。此次 Agent 任务未通过，无 post-fix verifier。
- Provider usage 共 8 次请求，97,699 输入 token、2,070 输出 token（合计 99,769）；费用不可用，未发现密钥泄漏。

这条候选已经具备来源、许可证和基线证据，但 Agent 未完成实现，不能计入通过数。再次评测时应明确要求用文件工具查看源码，并禁止 shell 执行任何非 `go test ./...` 命令。

## 12. 2026-09-26 Gin 3227 基线审核

候选 `gin-gonic__gin-3227` 对应 Gin Issue #2282 / PR #3227，固定 commit 为 `51aea73ba0f125f6cacc3b4b695efdf21d9c634f`，该 revision 的 `LICENSE` 为 MIT。运行环境为 macOS arm64、Go 1.27.0。

- 按公开 issue 复现，设置 `RedirectTrailingSlash=false` 与 `RedirectFixedPath=true` 后请求 `/ping/`，基线返回 301 并重定向到 `/ping`；issue 预期 404，故复现成立。
- 该次只做基线审核，未调用 Agent、未修改候选仓库，也没有 post-fix verifier。候选尚未通过。

## 13. 2026-09-26 Gin 1957 基线范围核对

候选 `gin-gonic__gin-1957` 对应 Gin Issue #1956 / PR #1957，固定 commit 为 `09a3650c97ca7ef3a542d428a9fb2c8da8c18002`，该 revision 的 `LICENSE` 为 MIT。

- 公开需求是把 HTTP Header 绑定到带 `header` struct tag 的结构体。固定 commit 的 `context.go` 和 `binding/` 中不存在 `ShouldBindHeader` 或 `HeaderBinding` API，属于新增功能候选。
- 本次只审核需求与基线 API，不包含 Agent 运行或 post-fix verifier；实现范围、类型转换与校验边界仍需先定义，因此不计通过。
