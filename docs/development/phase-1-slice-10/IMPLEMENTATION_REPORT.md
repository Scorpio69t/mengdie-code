# P1-10 实现报告

## 1. 交付结果

本切片完成了可直接用于排障的结构化 Doctor，以及不进入普通 CI 的 DeepSeek/Kimi 真实只读 smoke：

- `mengdie doctor --offline` 提供确定的零 Provider 网络本地诊断；
- 默认 `mengdie doctor` 通过固定 `doctor_echo` 请求验证端点、认证、SSE 和工具调用；
- 人类输出与 schema v1 JSON 覆盖配置、凭据存在性、AGENTS 链、Git、shell、rg、TTY、模型能力和稳定错误分类；
- 项目/用户配置路径脱敏，Provider URL 只报告 origin，密钥值不进入输出或在线请求；
- Doctor 的 Git 子进程使用最小环境，并禁用外部 diff、textconv、fsmonitor、锁和终端提示；
- DeepSeek/Kimi 示例已按 2026-08-06 官方资料更新，并拆分为可直接复制的独立配置；
- `liveprovider` build tag + `MENGDIE_LIVE_SMOKE=1` 构成真实 smoke 的双重 opt-in；
- GitHub 手动工作流绑定 `provider-smoke` Environment，在 macOS 与 Windows 上验证只读 Agent 闭环；
- 交互入口的过期“Agent 尚未实现”文案已改为准确引导用户使用 `exec` 和 `doctor`。

详细契约见 [`DOCTOR_AND_SMOKE.md`](./DOCTOR_AND_SMOKE.md)。

## 2. 安全与恢复门禁

- 离线模式在 Provider factory 前返回，测试证明不会构造 Provider；
- 在线探测不加载源码或用户任务，只提供一个无副作用固定工具；
- 探测超时限制为 1 秒到 1 分钟，默认 15 秒；
- Provider 原始错误正文不进入报告，只保留 category/code；
- 真实 smoke 不授予 edit/write/shell，限制 8 轮，并验证临时文件哈希语义不变；
- 真实 smoke 在输出失败分支前先执行密钥泄漏断言；
- 普通 push/PR 工作流不会读取 Provider 密钥或产生外部模型费用。

## 3. 测试与验证

本地环境：Windows amd64，Go 1.26.5。

- `go fmt ./...`：通过；
- `go vet ./...`：零问题；
- `go test ./...`：338 项通过，16 个 package；
- `go test -race ./...`：338 项通过，16 个 package；
- `golangci-lint run ./...`：0 issues；
- `govulncheck ./...`：未发现漏洞；
- `mengdie-eval --manifest evals/coding/smoke.json`：5/5 baseline 通过；
- `actionlint v1.7.7 .github/workflows/provider-smoke.yml`：通过；
- `go test -tags=liveprovider ...`：测试文件成功参与编译；未设置双重 opt-in 时按设计跳过；
- Doctor 人类输出与 `--offline --json` 本机演练：通过；
- `bd preflight --check`：5/6 通过，1 项版本检查因仓库不含 Beads 源码而跳过；另报告既有 `AGENTS.md` / `CLAUDE.md` 独立内容差异警告，本切片未改动两文件；
- `git diff --check`：通过。

真实 DeepSeek/Kimi 付费调用未在本地执行。合并后由维护者在受保护的 `provider-smoke` Environment 配置密钥并手动选择 Provider 运行，结果不作为普通 PR 的隐式前置条件。

## 4. 后续边界

本切片不把最小 smoke 等同于真实 Coding 验收。后续工作仍需覆盖交互会话、复杂修改、Shell、长任务、上下文压力和开发预览发布门禁；这些工作应继续以独立 Beads 小闭环推进。
