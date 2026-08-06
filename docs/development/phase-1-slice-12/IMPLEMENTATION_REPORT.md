# P1-12 实施报告：双平台真实 Provider Coding 预验收

## 交付结论

本切片新增了可重复的真实 Provider Coding 预验收入口，并在合并后完成 DeepSeek 付费实机套件。该证据只声明“有界双平台预验收通过”，不声明外部真实仓库任务或 M1 整体完成。

主要交付：

- `Provider 实机 Smoke` 新增 `readonly` / `m1-coding` 套件选择；
- DeepSeek、Kimi Code 与 Kimi 开放平台都在受保护 Environment 中覆盖 macOS、Windows，并使用互不混用的 Secret 与端点；
- `m1-coding` 在每个平台串行运行现有 5 个 Go fixture；
- 每项任务只获得项目内 edit/write 与 `go test` 窄授权；
- JSON Lines 必须证明成功的 read、edit/write、shell 和 run completion；
- Agent 结束后由独立 argv verifier 再次判断，不信任模型总结；
- manifest 通过 `acceptance.allowed_changes` 固定允许 diff，前后 SHA-256 拒绝测试篡改、依赖变化和未声明文件；
- 普通三平台 CI 离线编译并测试 build-tag Harness，不访问 Provider。

## Deep Agent 门禁

- 用户痛点：fake Provider 和交叉编译不能证明真实模型能完成双平台 Coding 闭环。
- 所属层次：Harness 评测；不改变 Runtime 自治级别。
- 上下文：每项任务使用独立临时 fixture，不共享历史；工具输出仍受 M1 限制。
- 权限：read 默认允许；write 仅本 run；execute 仅 `go test`；无网络工具或交互升级。
- 事实源：工作流 run、JSON Lines 事件与独立 verifier 退出码；模型文本不是事实。
- 中断与恢复：Job 60 分钟、测试 50 分钟、verifier 使用 manifest 超时；不自动重试付费任务。
- 明确不做：EventStore、resume、Artifact Store、外部真实仓库任务采集、TUI 和任何 M2 接口。

## 本地验证

以下门禁已通过：

- `go fmt ./...`；
- `go vet ./...`；
- `go test ./...`：349 tests / 16 packages；
- `CGO_ENABLED=1 go test -race ./...`：349 tests / 16 packages；
- build tag 下的证据解析、凭据环境过滤与无关 diff 拒绝：3 tests；
- `actionlint v1.7.12`：CI 与 Provider smoke workflow；
- `golangci-lint --build-tags=liveprovider`：0 issues，包含 errcheck；
- `govulncheck ./...`：未发现漏洞；
- `go build ./...`；
- `git diff --check`。

合并后真实证据：

- DeepSeek `readonly` 运行 `31078797034`：macOS、Windows 均通过；
- DeepSeek `m1-coding` 运行 `31079053820`：macOS 5/5、Windows 5/5；
- 未授权副作用、白名单外 diff 和密钥泄漏均为 0；
- Kimi `readonly` 运行 `31078807523`：双平台均返回 401。官方资料和用户确认表明使用的是 Kimi Code 会员 Key，而工作流错误指向开放平台端点；该结果促成 `kimi-code` / `kimi-platform` 显式拆分，不能计为 Kimi 通过。

## M1 当前边界

PR #20 合并后的 main CI 已形成连续第 20 次稳定性证据，DeepSeek 双平台 fixture 预验收也已通过；但 M1 仍需外部真实仓库任务和安全出口记录，不能仅凭 fixture 预验收完成。
