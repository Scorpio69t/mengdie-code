# 第一阶段 Slice 07 实现报告

## 结果

P1-07 已形成可测试的写文件最小闭环：`edit_file` 对已有文本执行确定性的精确替换，`write_file` 受控创建或显式覆盖完整文本文件。两者都复用 P1-04 的 PathGuard 与 P1-06 的一次性 Capability，不在模型提示词或 UI 层模拟安全控制。

协议细节见 [EDIT_WRITE_PROTOCOL.md](./EDIT_WRITE_PROTOCOL.md)。

## 交付

- Tool 协议：
  - 新增 `path_absent` 前置条件，创建目标在批准后出现时返回 `PreconditionError`；
  - `DefaultTools` 稳定注册 `edit_file` 与 `write_file`；
  - Prepare 继续通过规范参数 digest 绑定审批，Execute 必须先消费 Capability。
- `edit_file`：
  - 非空 `old_text`、确定的 `expected_replacements` 和严格 UTF-8 输入；
  - Prepare 校验匹配次数、计算新内容并生成有界 diff，记录原文件 SHA-256；
  - Execute 再解析路径、复核哈希与匹配次数，保留权限后原子替换；
  - 返回修改前后 SHA-256 和实际替换次数。
- `write_file`：
  - 默认只创建不存在文件，覆盖必须显式 `overwrite=true`；
  - 创建记录 `path_absent`，覆盖记录原文件 SHA-256 并展示完整 diff；
  - 缺失父目录计入审批标题，只在 Execute 阶段创建；
  - 返回操作类型、最终 SHA-256 和字节数。
- 共同落盘层：
  - 使用 Go 1.26 `os.Root` 将所有文件系统变更锚定到项目根句柄，防止目录在检查后被 symlink/junction 换出项目；
  - 同目录随机临时文件、权限设置、完整写入、`Sync`、关闭、最终前置条件复核；
  - 覆盖通过 `os.Root.Rename` 原子替换，创建通过 `os.Root.Link` 原子提交且不覆盖突然出现的目标；
  - 失败清理临时文件，并尽力回收本次创建且仍为空的父目录。
- 内容预算：编辑文件与编辑结果 1 MiB、单段精确替换 24 KiB、完整写入 32 KiB、diff 64 KiB；二进制、NUL、非法 UTF-8、目录和超限内容均拒绝。
- 未新增第三方依赖；diff、哈希、随机临时名和根目录能力全部使用 Go 标准库。

## 验证

- `go fmt ./...`：通过；
- `go vet ./...`：通过；
- `go test ./...`：通过；
- `go test -race ./internal/tools ./internal/platform ./internal/policy`：通过；
- `golangci-lint run ./...`：0 issues；
- `govulncheck@v1.1.4 ./...`：未发现漏洞；
- `mengdie-eval --manifest evals/coding/smoke.json`：5/5 baseline 通过；
- `bd preflight --check`：测试、lint、格式、Beads 污染与 go.sum 检查通过；保留仓库既有的 AGENTS.md/CLAUDE.md 差异警告，版本同步检查因本仓库没有 bd 自身源码而跳过；
- PR #13 的既有改动已通过 macOS、Windows、Ubuntu 三平台 CI 与质量检查；P1-07 的三平台 CI 将随本切片 PR 验证。

新增测试覆盖：精确匹配默认次数与显式多次替换、diff 与最终哈希一致、创建与显式覆盖、权限保留、Capability 缺失零副作用、内容变化/删除/类型变化、创建目标突然出现、symlink 目标改变、根句柄 symlink 越界、受保护路径与根外路径、未知字段、二进制/NUL/超限输入，以及失败后临时文件和父目录清理。

## Deep Agent 门禁落实

- 上下文：审批只接收受限 diff 和确定性元数据，不把二进制或超大文件注入上下文；
- 安全：Prepare 无副作用，写入只能发生在一次性 Capability 消费和最终前置条件检查之后；
- 恢复：本切片只保证单文件不暴露半写状态；Patch Journal、撤销和跨运行恢复留给 M2；
- 评测：正向落盘与关键负向失败路径均有自动化测试，后续 Agent Runtime 必须通过 fake model 的读/改/测闭环再接入真实模型。

## 明确不做

- `delete_file`、模糊编辑、正则编辑、多文件事务；
- Patch Journal、rewind、持久化恢复（M2）；
- `shell` 与进程适配（P1-08）；
- Agent Runtime 和 CLI 接线（P1-09）；
- daemon、Web、异步 Swarm、向量记忆和记忆图谱。
