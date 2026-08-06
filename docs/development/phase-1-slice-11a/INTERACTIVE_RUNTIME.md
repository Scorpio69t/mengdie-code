# P1-11A 交互运行协议

> 状态：实现完成，等待审核  
> 适用范围：M1 单进程交互入口

## 用户闭环

`mengdie [flags]` 只在 stdin 与 stdout 都连接终端时启动。它展示品牌与当前安全模式，读取一个不超过 64 KiB 的任务，创建一个内存 Run，然后把 Agent 事件和审批提示输出到 stdout。

```text
mengdie
→ 展示版本、模型、工作目录与审批模式
→ 读取一次任务
→ model → tool proposal → policy → approval → execute → observe
→ 输出最终结果和退出码
```

每个进程只执行一个任务。M1 不保存事件或上下文，因此当前没有历史会话、resume、REPL、后台 daemon 或完整 TUI；进程退出即丢失 `RunState`。自动化、管道与重定向必须使用 `mengdie exec`，交互入口不会在无法审批时静默退化。

## 输入与输出边界

- 任务按一行 UTF-8 文本读取，首尾空白会被移除，上限为 64 KiB；空输入、EOF 和超限都有确定错误。
- 任务读取器与 `TextBroker` 共享同一个 `bufio.Reader`，避免预读审批答案后丢失输入。
- 交互事件、工具预览和审批提示都走 stdout；错误诊断走 stderr。
- 事件不包含完整任务、密钥或隐藏推理；工具预览继续受各工具既有长度限制约束。
- Ctrl+C 沿用终端中断状态机：运行中第一次取消当前 Run，短时间内第二次强制退出。

## 审批规则

交互运行使用 `policy.ModeInteractive`：

| 操作 | 默认行为 |
|---|---|
| 普通项目读取 | Allow |
| 敏感读取 | Ask 或硬边界 Deny |
| edit/write | Ask |
| shell | Ask |
| 项目外路径、`.git` 写入、网络副作用 | Deny |

`approval = "auto-edit"` 只把项目内 `edit_file` 与 `write_file` 提升为本次运行的 Allow；Shell 仍由配置中的精确命令前缀规则或逐次审批决定。`y/是/允许` 批准，`n/否/拒绝` 拒绝，`e/编辑` 要求模型重新准备调用，不直接修改模型提交的参数。

批准产生一次性 Capability，Tool 在执行前仍需校验作用域、参数摘要、过期时间与 TOCTOU 前置条件。拒绝和“编辑后重试”作为结构化 Tool 结果返回模型；只要本次运行出现拒绝，最终进程退出码仍为 `4`，便于脚本识别 Policy 结果。

## 可验证性

固定 fake Provider 的应用级测试覆盖：

- 正常任务完成和人类事件渲染；
- edit 批准后才修改文件；
- edit 拒绝时文件不变，拒绝结果进入下一次模型请求；
- shell 批准后才执行；
- 非 TTY 在 Provider 构造前 fail-closed；
- 空任务、64 KiB 上限与预取消上下文。

macOS 与 Windows 的真实终端、进程树取消和开发预览产物由 P1-11B 完成最终出口验收，不能用本地 scripted input 测试替代。
