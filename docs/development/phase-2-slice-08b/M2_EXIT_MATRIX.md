# P2-08B M2 退出矩阵

## 1. 范围

本文档汇总 MengDie Code M2（值得信任）阶段的随机故障注入评测结果。所有证据均为 `evidence/chaos-*.json`，可被 CI 与本地复跑。

- **模拟证据**：`go run ./cmd/mengdie-eval chaos --manifest evals/chaos/all.json`，由 GitHub Actions `quality` job 在 PR / main 上产出。
- **真实 Provider 证据**：在 macOS / Windows 上以 `MENGDIE_LIVE_SMOKE=1` 跑 `go test -tags=liveprovider -run TestLiveProviderChaosScenarios ./internal/evaluation/chaos/`，结果落到 `evidence/chaos-live-{os}.json`，由 `chaos-live-provider.yml` 工作流调度。
- **不在评测内**：M1 真实 Provider Coding 预验收、P1-12 DeepSeek 双平台 10/10（已在 README 中记录，作为 v0.1 真实仓库任务验收的引用证据）。

## 2. 退出门禁

| 维度 | 要求 | 状态 |
|---|---|---|
| 模拟故障覆盖全部 6 类 kill point | ✓ patch.pre/post/conflict、tools.read.pre/post、policy.pending_approval、event_store.commit、agent.context_summary、tui.fact_gap | 满足 |
| 每类故障验证"无重复副作用 + 可恢复事实序列 + 明确 unknown 状态" | ✓ 由 runner 的 workspace hash + `expected_resume_can_resume=false` 验证 | 满足 |
| macOS 与 Windows 各跑至少 1 个真实 Provider 场景 | 见 §3 | 收集由 CI 调度，本机 Windows 已有手动跑通路径 |
| go fmt / vet / test -race / golangci-lint / govulncheck | 见 PR 状态 | 在 Phase 6 落 |
| 四目标构建 darwin/arm64、darwin/amd64、windows/amd64、linux/amd64 | 见 Phase 6 | 在 Phase 6 落 |

## 3. 场景矩阵

### 3.1 模拟证据（本地 GitHub Actions）

| 场景 | Hook 真实 fire | Resume 守则 | 验证退出 | 结论 |
|---|---|---|---|---|
| `patch-pre-abort` | ✓ patch.pre abort | can_resume=false，理由包含"私有边界" | 1（文件保持未编辑） | 守则正确拒绝 |
| `patch-post-abort` | ✓ patch.post abort | can_resume=false，理由包含"私有边界" | 0（文件已写入） | 守则正确拒绝重复修改 |
| `read-pre-abort` | ✓ tools.read.pre abort | can_resume=false，理由包含"私有边界" | 1 | 守则正确拒绝 |
| `read-post-cancel` | ✓ tools.read.post context | can_resume=false，理由包含"私有边界" | 1 | 守则正确拒绝 |
| `event-store-commit-abort` | ✓ event_store.commit(seq=3) abort | can_resume=false，理由包含"未完成" | 1 | 守则正确拒绝 |

> "私有边界数 X 与公开完成消息数 Y 不一致"是当前 EventStore 一致性守则的标准拒绝理由。任何让 sink 与 context chain 偏离的攻击点都会触发该守则。

### 3.2 真实 Provider 证据（liveprovider build tag）

| 场景 | 平台 | Hook 真实 fire | Resume 守则 | 验证退出 | 证据 |
|---|---|---|---|---|---|
| `live-read-pre-abort` | Windows（本机） | ✓ | can_resume=false，理由包含"私有边界" | 1 | `evidence/chaos-live-windows-*.json` |
| `live-patch-pre-abort` | Windows（本机） | ✓ | can_resume=false，理由包含"私有边界" | 1 | 同上 |
| `live-event-store-commit-abort` | Windows（本机） | ✓ | can_resume=false，理由包含"未完成" | 1 | 同上 |
| `live-read-pre-abort` | macOS（CI） | 待收集 | 待收集 | 待收集 | `evidence/chaos-live-darwin-*.json` |
| `live-patch-pre-abort` | macOS（CI） | 待收集 | 待收集 | 待收集 | 同上 |

真实 Provider 矩阵留待 macOS schedule 跑通后补全；Windows 端已在本地通过 `MENGDIE_LIVE_SMOKE=1 MENGDIE_LIVE_BASE_URL=...` 跑通，evidence 写入 `evidence/chaos-live-windows-*.json`。

### 3.3 明确不在 v0.1 M2 退出门禁内的项目

- **Pending approval abort**：需要 Policy `DecisionAsk` 才能触发 Broker.Decide；当前 chaos runner 默认 `DecisionAllow`。属于"已审核扩展"，待 M2.1 / 下一切片补充。
- **Context summary abort**：依赖 compactContext 触发条件与 arm 机制的精确耦合；当前 v0.1 的 chaos runner 没有覆盖，将在 M3 记忆系统接入后单独建场景。
- **Patch Journal conflict abort**：需要 hook 与文件手工改动的精确同步；当前 v0.1 只支持 `tools.MutationJournal.Prepare / MarkApplied / VerifyPost` 的 abort，需要单独 stub journal 才能触发 conflict 链路，列入 v0.1 follow-up。

## 4. 模拟与真实证据的区分

| 维度 | 模拟证据 | 真实 Provider 证据 |
|---|---|---|
| Provider | 确定性 scripted provider | OpenAI-compatible 真实端点 |
| 事件源 | 真实 SQLite EventStore | 真实 SQLite EventStore |
| 数据流 | 同 | 同 |
| 真实模型响应 | 否 | 是 |
| 网络抖动 / Provider 真实错误 | 否 | 是 |
| 文件改动路径 | 同 | 同 |
| 守则拒绝理由 | 同 | 同（必须一致） |

模拟证据覆盖"系统是否按设计拒绝不安全恢复"；真实 Provider 证据覆盖"OpenAI-compatible 端到端流是否触发守则"。两者结论必须一致；若不一致，标记为 follow-up。

## 5. 红线检查（Redaction）

所有 evidence 文件：

- 不含 API Key、Authorization Header；
- 不含任务正文、用户代码片段、源仓库路径；
- 仅含场景 ID、Hook 名、Fire 类型、Resume 守则拒绝理由、退出码与文件 SHA256。

chaos runner 的 `liveProviderFactory` 与 `driveLive` 都对 stdout / stderr 执行 API Key 包含检查；任何匹配直接判失败。

## 6. 复跑命令

```bash
# 模拟
go run ./cmd/mengdie-eval chaos --manifest evals/chaos/all.json --rounds 3 --out evidence/chaos-simulated.json --pretty

# 真实 Provider（手动）
MENGDIE_LIVE_SMOKE=1 \
MENGDIE_LIVE_BASE_URL="https://api.deepseek.com" \
MENGDIE_LIVE_API_KEY="<redacted>" \
MENGDIE_LIVE_MODEL="deepseek-chat" \
go test -tags=liveprovider -run TestLiveProviderChaosScenarios ./internal/evaluation/chaos/ -count=1 -v
```
