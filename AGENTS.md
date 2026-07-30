# MengDie Code 开发约定

## 项目定位

MengDie Code 是中文优先、记忆可验证、会复盘，并重点适配 macOS 与 Windows 的本地 Coding Agent。开始修改前先阅读 `README.md` 和 `ARCHITECTURE.md`。

## 当前范围

- 按 `ARCHITECTURE.md` 的 M0 → M4 顺序推进。
- 不把规划中的功能写成已经实现。
- daemon、Web、异步 Swarm、向量记忆和记忆图谱属于 v0.1 之后的候选能力。
- 优先完成可测试的小闭环，不提前稳定无调用方的公共接口。
- 第三方依赖遵循 `docs/DEPENDENCIES.md`：成熟度、维护状态、跨平台、许可证和供应链成本必须共同评估。

## Deep Agent 指导技能

- 进行架构设计、里程碑规划、Agent 能力实现或相关代码审查前，必须使用 `.agents/skills/build-mengdie-deepagent/SKILL.md`。
- 按该技能要求完整阅读 `references/principles.md` 与 `references/decision-gates.md`，把上下文、安全、恢复和评测门禁落实到当前切片。
- 技能用于吸收 Deep Agent 的 Harness 原则，不代表照搬 LangChain、LangGraph 或 Python API；已审核的 MengDie Code 产品范围和 Go 架构优先。

## 语言

- 用户文档、Issue 模板和产品文案默认中文优先。
- 英文贡献同样欢迎。
- Go 标识符、协议字段和代码注释使用清晰英文。
- README 的重要产品变化尽量同步到 `README_EN.md`。

## 检查

提交 Go 代码前运行：

```bash
go fmt ./...
go vet ./...
go test ./...
```

新增或升级依赖时还要运行 `govulncheck ./...`，并在 PR 中记录选型理由。

安全、事件、记忆 schema 和 Provider 兼容性变化必须带相应测试或明确的评测计划。
