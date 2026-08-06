# P1-11B 实现报告

## 交付结果

- 新增独立“开发预览”工作流，不改变 Provider、Agent、Policy 或 Tool 权限边界。
- macOS、Windows、Ubuntu 原生 runner 会构建并运行 `version` 与离线 Doctor。
- 生成 darwin/arm64、darwin/amd64、windows/amd64、linux/amd64 四个 unsigned 预览目标。
- 每个产物带 SHA-256、JSON 元数据与 Go build info，版本可追溯到 workflow run 和完整 commit SHA。
- 用户文档明确下载、校验、安装、卸载、未签名风险和当前功能边界。

## Deep Agent 门禁

- 所属层次：CI / 分发 Harness，不新增 Runtime 自治能力。
- 上下文：构建元数据只包含版本、commit、UTC 时间、GOOS/GOARCH 与 unsigned 标记，不包含源码、Prompt、路径或凭据。
- 安全：workflow 权限保持 `contents: read`，无 secret、无发布写权限、无外部上传。
- 恢复：Artifact 保留 7 天且不作为事实源；源 commit 与 workflow run 才是追溯入口。
- 降级：任何目标构建、元数据检查、checksum 或原生 smoke 失败，整个 workflow 对应 job 失败，不上传伪成功产物。

## M1 出口证据快照

截至 2026-08-06、P1-11B 开发开始前：

- `main` 的 CI push runs 连续成功 **18 次**，尚未达到设计要求的 20 次；
- 最新成功 run：`31068013487`，commit `93c8846be792e4c4eabeb8c4edd39a0f02675648`；
- 最早计入本次连续区间的 run：`30512102671`；
- PR #18 已证明质量、macOS、Windows、Ubuntu 四项 CI 全绿；
- 本机 Windows / Go 1.26.5 已完成全量测试、race、lint、漏洞检查和构建；
- macOS 与 Windows 的开发预览原生 smoke 需以本 PR 新 workflow 的结果补证。

因此本 PR 不把 M1 标记为完成。P1-11B 合并后，只有新增 workflow 全绿、`main` 连续成功达到 20 次、真实任务与安全出口验收均有证据，才允许进入 M2 完成状态。

## 本轮明确不做

- 不创建 GitHub Release 或正式版本标签；
- 不启用 Apple/Windows 签名、公证、Homebrew、Winget 或自动更新；
- 不增加 daemon、Web、EventStore、resume 或完整 TUI；
- 不用额外提交或重复运行刷高稳定性次数。

## 本地门禁记录

2026-08-06 在 Windows / Go 1.26.5 环境完成：

- `actionlint v1.7.12`：全部 GitHub Actions workflow 通过；
- 四目标 `CGO_ENABLED=0` 交叉构建及 GOOS/GOARCH/build info 检查通过；
- Windows 原生预览实际运行 `version` 与 `doctor --offline --json`，注入版本和 commit 一致；
- `gofmt -l .` 与 `git diff --check`：通过；
- `go vet ./...`：通过；
- `go test ./...`：16 个包、348 个测试通过；
- `CGO_ENABLED=1 go test -race ./...`：16 个包通过；
- `golangci-lint run ./...`：0 issue，默认启用 `errcheck`；
- `govulncheck ./...`：未发现已知漏洞；
- `go build ./...`：通过；
- Coding baseline：5/5 通过。

macOS、Windows、Ubuntu 原生 smoke、四份真实归档和 checksum 必须由本 PR 的新增 workflow 再验证；本地门禁不能替代这些远端证据。
