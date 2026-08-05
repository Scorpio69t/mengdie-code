# edit_file 与 write_file 协议

本文记录第一阶段 Slice 07 的稳定修改边界。目标是让用户批准的内容与实际落盘内容保持确定性，并在目标状态发生变化时安全失败。

## 工具边界

两个工具都声明 `write` Effect，必须经过 P1-06 的 Policy 与 Approval。`Execute` 的固定顺序是：

1. 消费绑定同一 RunID、ToolName 与 Digest 的一次性 Capability；
2. 用 `PathGuard` 重新解析写路径，并确认它与 Prepare 阶段的规范路径相同；
3. 检查批准时记录的前置条件；
4. 在目标目录准备完整的新内容，同步临时文件；
5. 再次检查前置条件，然后执行原子替换或原子创建。

Capability 缺失、路径改变、内容哈希改变、创建目标突然出现时，均不写入目标内容。

## edit_file

`edit_file` 只修改已有的 UTF-8 普通文件：

- `old_text` 必须非空，并与 `new_text` 不同；
- 默认要求精确匹配一次；需要批量替换时必须通过 `expected_replacements` 明确声明 1–1000 次；
- Prepare 记录原文件 SHA-256，并按实际替换次数生成确定性 diff 预览；
- Execute 重新验证哈希和精确匹配次数，保留目标文件权限位后替换；
- 不做模糊匹配、自动缩进、正则替换或“尽力而为”的部分修改。

单个 `old_text` / `new_text` 上限为 24 KiB，参与编辑的文件与编辑后文件上限为 1 MiB。diff 仍受统一的 64 KiB 工具输出预算约束。

## write_file

`write_file` 表达完整文件写入：

- 目标不存在时默认执行创建，并记录 `path_absent` 前置条件；
- 目标存在时必须显式传入 `overwrite=true`，Prepare 展示完整旧内容与新内容 diff，并记录原文件 SHA-256；
- `overwrite=true` 不能用于不存在的目标，避免调用意图含糊；
- 新文件默认权限为 `0644`，覆盖时保留已有权限位；
- 创建缺失父目录属于同一个 PreparedCall，并在审批标题中显示层数。

一次完整写入上限为 32 KiB，只接受不含 NUL 的 UTF-8 文本。覆盖文件的完整 diff 超过 64 KiB 时拒绝 Prepare，应改用更聚焦的 `edit_file`。

## 原子落盘

新内容先写入目标同目录的 `.mengdie-write-*` 临时文件，设置权限、`Sync` 并关闭后才提交：

- 所有创建、链接、替换和清理都通过 Go 1.26 的 `os.Root` 锚定在项目根句柄内，目录在检查后被换成 symlink/junction 也不能把写入带出项目；
- 覆盖使用同目录 `os.Root.Rename`，由 macOS/Unix rename 与 Windows ReplaceIfExists 语义完成原子替换；
- 创建使用同目录硬链接提交，使“目标必须不存在”与提交成为同一个原子动作；
- 任一提交前错误都会清理临时文件，并尽力逆序清理由本次调用新建且仍为空的父目录；
- 如果底层文件系统不支持硬链接，创建会显式失败，不退化为可能留下半文件的直接写入。

这些措施防止用户观察到半写文件，并把批准后的常见外部修改收敛为 `PreconditionError`。底层文件系统或其他进程不受 MengDie Code 控制；协议不宣称提供事务型文件锁。

## 路径与内容安全

- Prepare 和 Execute 都经过 `PathGuard`；项目根外、`.git`、`.mengdie`、凭据文件、Windows 设备路径、UNC、ADS 等沿用 P1-04 的硬拒绝规则；
- 已存在路径会解析 symlink，新路径会解析最近的已存在祖先；Execute 发现规范路径与批准路径不同即拒绝；
- 二进制、非法 UTF-8、NUL、目录和超限内容不会进入修改流程；
- 参数严格解码，未知字段直接报错。

## 当前不做

- Patch Journal、撤销、回滚和断点恢复（M2）；
- 多文件事务和补丁批次；
- 二进制编辑、超大文件流式改写；
- daemon、Web、异步 Swarm、向量记忆和记忆图谱；
- Agent Runtime 接线（P1-09）。
