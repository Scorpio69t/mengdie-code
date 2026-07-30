# Task Plan：第一阶段 Slice 02

## Goal

交付 P1-02 的稳定事件协议、人类终端输出、JSON Lines 输出和可测试的 Ctrl+C 状态机，为 Provider 与 Agent Runtime 提供不依赖具体 UI 的可观察边界。

## Phases

- [x] Phase 1：合并 Deep Agent 指导 Skill，同步受保护主干并冻结 P1-02 范围
- [x] Phase 2：审计现有 App、品牌入口、测试与跨平台终端边界
- [x] Phase 3：实现版本化事件模型、验证规则和并发安全的内存 Sink
- [x] Phase 4：实现人类 renderer、JSON Lines renderer 与输出脱敏/错误传播
- [x] Phase 5：实现第一次取消、短窗口第二次退出的中断状态机和 scripted 测试
- [x] Phase 6：接入 App 可运行预览，补齐中英文文档与端到端测试
- [ ] Phase 7：运行完整校验并发布草稿 PR

## Key Questions

1. 哪些事件字段现在必须稳定，才能让 P1-03/P1-09 使用而不提前实现 M2 持久化？
2. 人类输出、JSON Lines 和未来 TUI 怎样共享同一事件源而不让 Agent 直接打印？
3. Ctrl+C 怎样在无真实 Provider 的情况下确定性测试第一次取消与第二次退出？
4. 哪些路径、错误和字段必须在 renderer 边界脱敏？

## Decisions Made

- 本切片只完成 P1-02，不实现 Provider、Agent 循环、EventStore、完整 TUI 或平台进程树终止。
- 事件属于 Harness 边界：保留 RunID、Seq、Version、Time、Kind 与类型化 Payload，M2 再增加 SessionID 和持久化。
- Renderer 只消费事件，不读取模型、工具或 stdin；审批与真实进程取消留给后续工作包。
- Ctrl+C 状态机依赖可注入时钟和 cancel/exit 回调，避免睡眠型测试。
- 采用 Go 标准库，除已有 TOML 依赖外不新增第三方组件。

## Errors Encountered

- 收紧 `run.started` 隐私字段后，一处 renderer 测试夹具仍引用已删除的 `Task` 字段；已修正并重新通过定向与全量测试。

## Status

**Currently in Phase 7** — 本地完整质量门禁通过，正在做最终 diff 审查并发布草稿 PR。
