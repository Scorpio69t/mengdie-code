# P2-07 实施报告：项目指令与最小 Skill

## 交付范围

本切片保留既有 AGENTS.md 加载链，并补齐最小本地 Skill 闭环：

- 用户级目录为 `~/.mengdie/skills/<name>/SKILL.md`，项目级目录为 `.mengdie/skills/<name>/SKILL.md`；
- 用户级先发现，项目级同名 Skill 确定性覆盖用户级版本；
- Runtime 常驻上下文只包含名称、单行 `description` 和逻辑来源；
- Provider 仅能用 `read_skill({"name":"..."})` 按需读取当前发现快照中的完整正文；
- `doctor` 的 JSON 与普通输出展示生效来源和同名冲突，不展示本机绝对路径；
- 不支持远程安装、脚本自动执行或任何隐式授权。

## 输入与预算边界

`SKILL.md` 必须是普通 UTF-8 文件，并以包含 `name` 与单行 `description` 的 frontmatter 开头。名称限制为可移植的小写字母、数字、短横线与下划线，且必须与目录名一致。单文件上限为 48 KiB，单次发现总读取上限为 1 MiB，最终生效 catalog 最多 64 项，description 上限为 2 KiB。

发现过程会解析符号链接后的真实路径，并拒绝逃离对应用户级或项目级 Skill 根的文件。`read_skill` 的模型参数只有 catalog 名称；Prepare 与 Execute 都重新读取并核对发现时的 SHA-256，路径或内容在运行期间变化时 fail-closed，要求开启新 Run 重新发现。

## 权限与隐私

`read_skill` 明确声明为只读工具，仍通过统一 Policy、Authorizer 和一次性 Capability 边界。Skill 正文可以指导模型选择工作方式，但不能放宽文件、命令、网络或其他工具权限。发现和读取均不产生网络请求，也不会执行 Skill 目录中的脚本。

发送给 Provider 和 doctor 展示的来源使用 `~/.mengdie/...` 与 `$PROJECT_ROOT/...` 逻辑路径。绝对路径不进入 Skill catalog 提示、公开事件、TUI 或 JSONL；完整正文只在模型明确调用 `read_skill` 后进入私有上下文。

## 验证

合并前运行：

```bash
go fmt ./...
go vet ./...
go test ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
```

测试覆盖用户/项目优先级、同名冲突、稳定排序、frontmatter 与大小拒绝、按需读取、未知参数拒绝、快照变化拒绝、Runtime 只注入元数据，以及 doctor 路径脱敏。

本地验证结果：`go vet ./...` 无问题，`go test ./...` 共 19 个 package、539 项测试通过，`govulncheck@v1.1.4` 报告无漏洞；`darwin/arm64`、`darwin/amd64`、`windows/amd64` 与 `linux/amd64` 的 `CGO_ENABLED=0 go build ./cmd/...` 均通过。
