# MengDie Code 依赖与现代化工程准则

> 状态：第一阶段准入规则
>
> 原则：成熟、活跃、跨平台、可替换；流行度是门槛，不是唯一决策依据。

## 1. 什么叫“现代化”

MengDie Code 不用“依赖越多”证明现代化。现代化意味着：

- 使用当前受支持的 Go 版本、标准库能力和清晰的 `context.Context` 取消链；
- 显式错误语义、结构化事件、依赖注入和小接口，而不是全局单例；
- macOS 与 Windows 同时设计、同时测试，不把其中一个当作兼容层；
- 默认安全、可观测、可测试，并保留升级和替换第三方组件的边界；
- CLI 人类输出与 JSON/管道输出分离，终端表现不破坏自动化。

## 2. 第三方组件准入门槛

引入运行时依赖前，PR 必须记录：

1. **真实必要性**：标准库或现有依赖为什么不足。
2. **社区采用度**：项目有广泛真实用户，不依赖短期热度或单一作者宣传。
3. **维护状态**：近期仍有发布、Issue 响应或安全维护；归档项目默认拒绝。
4. **跨平台证据**：macOS arm64/x64、Windows x64 和 Linux CI 可验证。
5. **许可证**：默认接受 Apache-2.0、MIT、BSD；其他许可证必须单独审核。
6. **供应链成本**：检查直接与传递依赖、二进制体积、CGO、网络行为和已知漏洞。
7. **可替换性**：第三方类型不得无边界扩散到核心领域模型。

禁止仅凭 GitHub Star 数选型。流行但过重、维护停滞、依赖树失控或破坏单二进制分发的组件同样不采用。

## 3. 第一阶段组件策略

| 能力 | 第一阶段选择 | 状态与原因 |
|---|---|---|
| CLI 参数 | Go `flag.FlagSet` | 已确定；M1 命令较少，标准库足够且零依赖 |
| HTTP / SSE | `net/http`、`bufio.Reader` | 已确定；便于精确控制国内兼容端点与流式错误 |
| 结构化日志 | `log/slog` | 已确定；标准库、结构化、可替换 Handler |
| TOML | [`pelletier/go-toml/v2`](https://github.com/pelletier/go-toml) | P1-01 候选；成熟且 v2 API 清晰，需先做严格解析测试 |
| 完整 TUI | [`charmbracelet/bubbletea`](https://github.com/charmbracelet/bubbletea) + Lip Gloss | 保留候选；M1 先用简单 renderer，只有交互复杂度达到阈值才引入 |
| SQLite | [`modernc-org/sqlite`](https://github.com/modernc-org/sqlite) | M2 首选候选；纯 Go 有利于 macOS/Windows 单二进制，采用前验证体积与性能 |
| 系统凭据 | OS 原生适配器，评估 [`zalando/go-keyring`](https://github.com/zalando/go-keyring) | M1 小版本候选；必须验证 Keychain 与 Credential Manager 的失败语义 |
| 终端能力 | `golang.org/x/term` | 需要 TTY、尺寸或安全输入时采用，避免自行维护平台探测 |
| Windows 进程树 | [`golang.org/x/sys/windows`](https://pkg.go.dev/golang.org/x/sys/windows) v0.47.0 | P1-08 已采用；Go 官方维护、BSD-3-Clause、无新增传递依赖且无 CGO，只在 Windows 构建中封装 Job Object API，领域层不暴露其类型 |
| 测试比较 | [`google/go-cmp`](https://github.com/google/go-cmp) | 仅在复杂结构断言出现时作为测试依赖引入 |

候选不等于已经依赖。每个组件只在对应工作包真正需要时，通过独立 spike 和 ADR 固化版本。

## 4. 持续治理

- `go.mod` 只保留直接使用的依赖，提交前运行 `go mod tidy`。
- CI 运行 `go vet ./...`、`go test -race ./...` 和 `govulncheck ./...`。
- Go 最低补丁版本跟随当前受支持分支的安全修复；P1-03 引入真实 TLS/HTTP 调用后，基线提升为 Go 1.26.5，以覆盖 `crypto/tls`、`crypto/x509`、`net/http`、`net/textproto` 与 Windows `net` 的已知修复。
- Dependabot 每周检查 Go Modules 与 GitHub Actions，次版本和补丁版本合并为批量 PR；升级仍需通过完整回归。
- 发布时生成 SBOM、校验和与依赖许可证清单。
- 大版本升级单独提交，不与业务功能混合，保留明确回滚点。
