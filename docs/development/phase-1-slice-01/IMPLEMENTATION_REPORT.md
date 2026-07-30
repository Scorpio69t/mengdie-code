# 第一阶段 Slice 01 实现报告

> 状态：完成，远端 CI 通过，等待审核

草稿 PR：[Scorpio69t/mengdie-code#5](https://github.com/Scorpio69t/mengdie-code/pull/5)

## 交付

### P1-00 · M0 评测入口

- `cmd/mengdie-eval` 机器可读评测命令；
- schema version 1 的严格 JSON manifest；
- 临时目录隔离、路径逃逸防护、symlink 拒绝、argv 直接执行和 64 KiB 输出上限；
- 五个独立 Go Coding fixture，覆盖边界、字符串、集合、路径和数值精度；
- CI 自动验证五个 fixture 的预期失败 baseline。

### P1-01 · App 与配置骨架

- `internal/app` 命令分发，支持根命令、`version`、最小 `doctor` 和诚实失败的 `exec` 占位；
- `internal/project` Git 项目根发现；
- defaults < user TOML < project TOML < environment < CLI 的配置优先级；
- `go-toml/v2` 严格字段解析、profile 校验和内联 `api_key` 拒绝；
- `doctor --json` 只显示凭据环境变量名和存在性，不回显秘密；
- DeepSeek / Kimi 无密钥配置样例。

## 设计偏差

- P1-00 当前只提供 baseline 模式；调用真实 Agent 的 candidate 模式要等 Agent Runtime 接口稳定后加入。
- doctor 当前不访问网络，也不检查 Provider 认证；完整探测仍属于 P1-10。
- `exec` 已完成命令解析和配置加载，但明确返回 Agent Runtime 尚未实现，不伪造任务成功。

## 本地验证

- `go test ./...`：19 项通过；
- `go test -race ./...`：CGO + GCC 13.1.0 下 19 项通过；
- `go vet ./...`：通过；
- `govulncheck`：0 个代码可达漏洞；
- Coding baseline：5/5 符合预期；
- `mengdie doctor --cwd . --json`：输出有效且无秘密值。

## 远端验证

- 首轮 Linux、macOS、Windows 单元测试全部通过；
- 质量检查识别出的 CRLF 问题已修复，`gofmt` 门禁通过；
- `go vet`、`govulncheck`、Race 测试和 Coding baseline 全部通过；
- 项目最低 Go 补丁版本提升至 1.26.1，规避 GO-2026-4601 与 GO-2026-4602。

## 下一切片

进入 P1-02：定义事件类型、人类输出与 JSON Lines renderer，并为 Ctrl+C 状态机建立 scripted I/O 测试；随后再接 P1-03 OpenAI-compatible Provider。
