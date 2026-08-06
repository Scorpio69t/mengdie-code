# P1-11A 实现报告

## 交付结果

- 裸命令不再显示占位提示，而是运行一次有界的交互 Agent 任务。
- Application Service 为交互与无头模式装配同一 Agent Runtime，并显式传递 Policy mode、Broker 与安全说明。
- stdin/stdout 任一不是终端时，交互模式在加载配置和构造 Provider 前拒绝执行。
- `TextBroker` 显式复用已有 `bufio.Reader`，保证任务输入产生的预读数据不会吞掉审批回答。
- 中文 README、英文 README 与详细设计同步了当前能力和未实现边界。

## 安全与恢复门禁

- 所有副作用仍走 Prepare → Policy → Approval → one-shot Capability → Execute。
- 项目外路径、`.git` 写入和未支持的网络副作用没有新增绕过路径。
- 本切片没有持久化恢复能力；事件带有 RunID/Seq，但进程退出后不宣称可恢复。
- 交互失败沿用稳定退出码，拒绝不会被最终模型文本掩盖。

## 验收边界

本报告只证明本地确定性测试和仓库质量门禁。真实 macOS/Windows 终端 smoke、连续 20 次 main CI 与 unsigned 开发预览产物属于 P1-11B，完成前 M1 仍不标记为结束。

## 本地门禁记录

2026-08-06 在 Windows / Go 1.26.5 环境完成：

- `gofmt -l .`：无未格式化文件；
- `git diff --check`：通过；
- `go vet ./...`：通过；
- `go test ./...`：16 个包、348 个测试通过；
- `CGO_ENABLED=1 go test -race ./...`：16 个包通过；
- `golangci-lint run ./...`：0 issue，启用的默认检查包含 `errcheck`；
- `govulncheck ./...`：未发现已知漏洞；
- `go build ./...`：通过；
- Coding baseline：5/5 通过。

PR 上的 macOS、Windows 与 Ubuntu CI 是合并门禁，不以这份本地记录代替。
