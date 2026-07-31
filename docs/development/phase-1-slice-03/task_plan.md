# Task Plan：第一阶段 Slice 03

## Goal

交付可被后续 Agent Runtime 使用的 OpenAI-compatible Provider：稳定类型、HTTP/SSE 流、tool call 组装、错误分类、有限重试和无外网依赖的契约测试。

## Phases

- [x] Phase 1：合并 P1-02，同步受保护主干并冻结 P1-03 范围
- [x] Phase 2：核验官方协议与现有配置/事件边界，记录兼容性风险
- [x] Phase 3：实现 Provider 领域类型、能力声明与错误 taxonomy
- [x] Phase 4：实现有界 SSE parser、流式文本与 tool call assembler
- [x] Phase 5：实现 HTTP client、认证、取消、超时与“可见输出前”有限重试
- [x] Phase 6：完成 httptest 契约矩阵、负向与泄密测试
- [x] Phase 7：更新 Provider 协议、架构状态与中英文入口；保持 CLI 未接入事实可见
- [x] Phase 8：运行完整验证并发布草稿 PR

## Key Questions

1. 怎样支持常见 `/chat/completions` 兼容端点，同时不把供应商差异伪装成完全一致？
2. 哪些流式事件属于“已对 Agent 可见”，从而禁止自动重放？
3. 如何在 CRLF、多 `data:` 行、2 MiB 上限、非法 JSON 与异常 EOF 下保持确定行为？
4. tool call 的 index、id、name 与 arguments 分片如何组装并校验？
5. 错误里保留哪些诊断信息，同时不泄露 Authorization、完整 prompt 或响应正文？

## Decisions Made

- 本切片只完成 P1-03，不实现 Agent loop、工具执行、Policy、真实 Provider smoke 或 capability 猜测。
- 采用 Go 标准库 `net/http` 与 `bufio.Reader`，不新增运行时依赖。
- Provider 只承诺显式声明的能力；兼容端点差异通过配置与错误暴露，不运行中猜测。
- 自动重试仅发生在任何 text/tool delta 交付给 Sink 之前；一旦可见，断流直接失败。
- 请求体、Authorization、完整响应正文和 reasoning 内容不进入错误或事件。

## Errors Encountered

- `rtk Get-Content` 无法执行 PowerShell 内建命令；改为 `rtk powershell -NoProfile -Command ...`。
- 仓库未初始化 `.beads` 数据库，`bd prime` 明确返回 no beads database；未擅自初始化，继续以本阶段文档记录本轮进度。
- PowerShell 双层命令中的 `$lines` 被外层 shell 提前展开；改用 `rg -A/-B` 读取上下文。
- 本机 Go 1.26.2 的 `govulncheck` 检出 5 个标准库可达漏洞；官方发布记录确认 Go 1.26.5 含最新 `crypto/tls` 安全修复，最低版本已提升并重新验证。

- 分段读取设计文档时，嵌套 PowerShell 命令中的变量被外层 shell 展开；改用带范围的 `rg` 读取，未修改工作区。
- Provider 负向测试表的一项复合字面量缺少收尾大括号，首次编译失败；修正后重新运行定向测试。

## Status

**Completed** — PR #8 已发布为草稿，本地与首轮 macOS/Windows/Ubuntu/质量 CI 全部通过，等待人工评审。
