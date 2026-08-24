# P2-08B 实施报告：随机故障注入与 M2 退出评测

## 交付范围

本切片把 M2 的"中断后仍可解释、可恢复、可回滚"承诺落到一组可复现的故障注入场景与一份 M2 退出矩阵：

- 新增 `internal/evaluation/chaos` 包，提供事件、Provider、Broker、Tool Registry、Mutation Journal、FactBus 的装饰器与确定性调度器；
- 新增 `evals/chaos/all.json`（6 个模拟场景）与 `evals/chaos/live.json`（3 个真实 Provider 场景）；
- 新增 `cmd/mengdie-eval chaos` 子命令，支持 `--manifest / --rounds / --seed / --out / --pretty`；
- 新增 `internal/evaluation/chaos/runner_test.go`（模拟端到端）与 `internal/evaluation/chaos/live_provider_test.go`（liveprovider build tag）；
- 新增 `docs/development/phase-2-slice-08b/M2_EXIT_MATRIX.md` 与本报告。

## 故障注入设计（最小装饰器风格）

### Hook 名与守则

| Hook | Fire 类型 | 期望守则行为 |
|---|---|---|
| `event_store.commit` | abort / context | sink 持久化边界被攻击；一致性守则拒绝后续恢复 |
| `agent.context_summary` | abort / context | 上下文摘要 Provider 调用被攻击；fallback 重试 |
| `policy.pending_approval` | abort / context | Broker 决定被攻击；同上 |
| `tools.read.pre` | abort / context | 只读工具 Execute 前被攻击；可安全重试 |
| `tools.read.post` | abort / context | 只读工具 Execute 后被攻击；同上 |
| `patch.pre` | abort / context | MutationJournal.Prepare 被攻击；写前阻断 |
| `patch.post` | abort / context | MarkApplied 后被攻击；写已落盘，拒绝重复修改 |
| `patch.conflict` | abort / context | VerifyPost 触发 conflict；守则拒绝任何后续写 |
| `tui.fact_gap` | unknown | TUI 订阅缺口；按需回放 |

调度器使用 `math/rand` 兼容的 `Schedule{Seed, Fires}` 模型，每条 `PlannedFire` 描述 `(Hook, FireKind, AfterSeq)`，每 hook 至多一次 fire（或多次 EventStoreCommit），并支持 `Arm(hook, kind)` 单次显式触发。

### 生产代码零改动

所有 hook 都通过装饰器包住既有接口：

```go
chaosSink := chaos.NewSink(realEventSink, ctrl)
chaosProvider := chaos.NewProvider(realProvider, ctrl)
chaosJournal := chaos.NewJournal(realJournal, ctrl)
chaosRegistry, _ := chaos.WrapRegistry(realRegistry, ctrl)
chaosBroker := chaos.NewBroker(realBroker, ctrl)
```

production 代码未改一行；hook 触发在测试 / chaos 驱动里以装饰器形式出现。

## 场景与验证契约

| 场景 | Hook | Fire | 期望恢复守则 | 期望退出码 |
|---|---|---|---|---|
| `patch-pre-abort` | patch.pre | abort | can_resume=false / 理由含"私有边界" | 1（文件未编辑） |
| `patch-post-abort` | patch.post | abort | can_resume=false / 理由含"私有边界" | 0（文件已写入） |
| `read-pre-abort` | tools.read.pre | abort | can_resume=false / 理由含"私有边界" | 1 |
| `read-post-cancel` | tools.read.post | context | can_resume=false / 理由含"私有边界" | 1 |
| `event-store-commit-abort` | event_store.commit(seq=3) | abort | can_resume=false / 理由含"未完成" | 1 |

每个场景的验证契约：

1. Hook 真实 fire（runner 通过 `Controller.Observations()` 校验）；
2. Resume 守则拒绝条件（`expected_resume_can_resume` 与 `expected_resume_reason_contains`）；
3. 验证命令退出码与期望一致；
4. `workspaceHash` 与"无 chaos 好结果"对比（由 `goodRunBaseline` 计算）。

## 真实 Provider 证据采集

`liveprovider` build tag 下的 `TestLiveProviderChaosScenarios` 读 `evals/chaos/live.json`，按与模拟场景一致的脚本驱动 Agent，但 Provider 是真实 OpenAI-compatible 端点。采集结果（Hook fire 列表、Resume 守则拒绝理由、验证退出码、workspace SHA256）落到 `evidence/chaos-live-{os}-*.json`，并对 stdout/stderr 做 API Key redaction 检查。

macOS 与 Windows 的真实证据通过 GitHub Actions `chaos-live-provider.yml`（matrix: macos-latest / windows-latest，schedule + workflow_dispatch）调度；本机 Windows 通过 `MENGDIE_LIVE_SMOKE=1` 手动触发。

## 验证

本地验证结果（待 PR 阶段补 CI 完整截图）：

- `go fmt ./...`：通过；
- `go vet ./...`：无问题；
- `go test ./internal/evaluation/chaos/ -count=1 -race`：通过；
- `go run ./cmd/mengdie-eval chaos --manifest evals/chaos/all.json --rounds 1 --pretty`：通过，6/6 场景达成守则预期；
- `go build -tags=liveprovider ./internal/evaluation/chaos/`：通过；
- `go vet -tags=liveprovider ./internal/evaluation/chaos/`：通过。

CI 集成与四目标构建、govulncheck、golangci-lint 由 Phase 5 与 Phase 6 完成。

## 已知遗留 / v0.1 follow-up

- `patch.conflict` 场景需要独立 stub journal 才能触发 conflict 链路，列入 v0.1 follow-up；
- `policy.pending_approval` 与 `agent.context_summary` 场景需要更细的脚本编排，分别在 M3 记忆接入时补充；
- 命令 ID 幂等阻断（`command-id-duplicate`）目前在 baseline eval 已经验证；M2 退出矩阵里仅作为引用证据，不重复列入 chaos 场景。

## 红线检查

- chaos runner 与真实 Provider 测试均对 stdout / stderr 做 API Key 包含检查；
- evidence JSON 不含任务正文、用户代码或源仓库路径；
- 任何 redaction 命中直接判失败。

完整证据与跨平台汇总见 `docs/development/phase-2-slice-08b/M2_EXIT_MATRIX.md`。
