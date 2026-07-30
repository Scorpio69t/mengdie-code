# Notes：第一阶段 Slice 01

## 当前事实

- 第一阶段详细设计已审核并合并。
- P1-00 要求任务 manifest、fixture 规范、结果 schema 和首批自动任务。
- P1-01 要求子命令、配置加载、profile 校验、项目根和版本信息。
- macOS 与 Windows 是 Tier 1；Linux 是持续构建支持的 Tier 2。
- 当前 CLI 已有品牌启动页和构建信息，但 Agent 功能尚未实现。

## 待验证

- [x] 本机 CGO 已永久开启，GCC 13.1.0 与 Race Detector 可真实运行。
- [x] 主干已包含品牌 PR 提交 `3c7814b`，本地与 `origin/main` 对齐。
- [x] 详细设计中的配置字段、路径与命令约束足够支持 P1-00 / P1-01；完整 doctor、Provider 与工具执行留在后续工作包。

## Slice 01 接口决策

- `evals/coding/smoke.json` 保存 suite 与 task manifest，严格拒绝未知字段。
- 每个 fixture 是独立小型 Go module；baseline 运行 `go test ./...` 并验证预期非零退出。
- runner 复制 fixture 到临时目录，避免污染仓库；命令使用 argv 数组，不调用 shell。
- 配置顺序：defaults < user TOML < project TOML < environment < CLI。
- TOML 库选择 `github.com/pelletier/go-toml/v2 v2.4.3`；2026-07-05 发布的当前稳定版本，最低 Go 要求低于本项目。

## 实现记录

### P1-00

- 新增 `cmd/mengdie-eval` 与 `internal/evaluation`。
- manifest 使用 schema version 1、严格 JSON、唯一 task ID 和显式超时。
- runner 直接执行 argv、限制单路输出 64 KiB、强制 `GOWORK=off`、拒绝 fixture 路径逃逸和 symlink。
- 首批五个 Go fixture 覆盖边界、字符串、稳定去重、路径与数值精度。
- baseline 实测 5/5 符合预期，机器可读 JSON 正常输出。

### P1-01

- 新增 `internal/project.FindRoot`，支持 `.git` 文件或目录并在非 Git 目录安全回退。
- 新增 defaults < user < project < environment < CLI 的配置合并。
- TOML 使用 `go-toml/v2 v2.4.3` 严格解析，显式拒绝任何 profile 中的 `api_key`。
- `doctor --json` 只报告凭据环境变量名和是否存在，不读取或回显值。
- 根命令、`version`、`doctor`、`exec` 已由 `internal/app` 分发；`exec` 明确返回尚未实现。
- 当前全量测试 19 项通过，`go vet` 通过。

### Phase 5 验证

- 增加 DeepSeek / Kimi 无密钥配置样例，并由测试真实解析。
- CI 质量任务增加 Coding baseline。
- 本机 `go test ./...`：20 项通过。
- 本机 `go test -race ./...`：CGO 开启后 20 项通过。
- `go vet ./...`：通过。
- `govulncheck`：0 个代码可达漏洞。
- Coding baseline：5/5 符合预期。
- `mengdie doctor --cwd . --json`：输出有效且不包含秘密值。

