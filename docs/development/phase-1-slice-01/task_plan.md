# Task Plan：第一阶段 Slice 01

## Goal

交付可测试的 P1-00 评测骨架和 P1-01 应用/配置骨架，使 MengDie Code 从品牌占位入口进入可持续迭代的第一条纵向开发切片。

## Phases

- [x] Phase 1：同步主干并配置本地 CGO / Race 工具链
- [x] Phase 2：核对详细设计，冻结 Slice 01 范围与接口
- [x] Phase 3：实现 P1-00 评测 manifest、fixture runner 与机器可读结果
- [x] Phase 4：实现 P1-01 命令分发、配置加载、项目根和版本信息
- [x] Phase 5：补齐单元测试、双平台边界测试和开发文档
- [ ] Phase 6：运行完整校验并发布草稿 PR

## Key Questions

1. 怎样让评测骨架现在就能运行，同时不假装 Agent Runtime 已实现？
2. 配置层怎样支持 macOS 与 Windows 的路径优先级，又不提前引入凭据和 Provider 复杂度？
3. 哪些第三方组件在本切片确有必要，哪些应继续使用现代 Go 标准库？

## Decisions Made

- 本轮只完成 P1-00 与 P1-01，不进入 Provider、工具执行和 TUI。
- 计划文件放在 `docs/development/phase-1-slice-01/`，不修改已审核通过的设计计划状态。
- 评测 manifest 使用 JSON；runner 直接执行 argv，不经过 shell，baseline 模式验证 fixture 的预期失败状态。
- 配置使用 `github.com/pelletier/go-toml/v2 v2.4.3` 严格解析；配置层保持与第三方类型隔离。
- 本切片的 `doctor` 只展示配置、项目和凭据存在性，不提前实现 P1-10 的网络探测。

## Errors Encountered

- `go test -race` 首次开发前发现 CGO 关闭：已执行 `go env -w CGO_ENABLED=1`，确认 GCC 13.1.0 可用，Race 测试通过。
- Go 工具规范化了 `go.mod` 末尾空行：属于格式变化，将在提交前决定恢复或随首个实现提交统一。
- P1-01 首次编译发现 `internal/app/doctor.go` 有未使用的 `flag` import：已移除并重新运行全量检查。
- App 重构后的状态文案破坏两项既有入口测试：恢复“Agent 功能尚未实现”，保持中文产品提示兼容。
- 最终审阅发现 Markdown 尾随空格、冗余 TOML 尾部读取和会受用户配置影响的 cmd 测试：均已清理，行为测试移入可注入环境的 `internal/app`。
- Git HTTPS 推送两次无法连接 `github.com:443`：改用 GitHub API 发布 44 个文件，首次 tree 构建暴露换行重编码 SHA 假设错误，随后完全采用 API 返回的 blob SHA 成功创建 PR #5。
- 首轮远端三平台测试全部通过，但质量检查发现 API 上传绕过 `.gitattributes`，新 Go 文件为 CRLF：已按原始字节重新发布为 LF，并核对远端 blob SHA 与本地一致，等待复检。

## Status

**Currently in Phase 6** — 审阅完整差异、运行最终校验并发布草稿 PR。
