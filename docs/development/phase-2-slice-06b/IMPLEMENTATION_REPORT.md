# P2-06B 实施报告：回滚材料与安全 rewind

## 结论

本切片提供应用层命令 `mengdie rewind <session-id>`。它只处理一个已经 `verified` 且尚未回滚的单文件 Patch Journal；默认选择最近目标，也可通过 `--journal-id` 精确指定。命令只能在交互终端运行，必须查看写后到写前的聚焦 diff 并显式批准。无头、重定向、路径变化、用户后续编辑或权限变化全部 fail-closed。

M2 仍不标记完成：同一 TUI 连续多轮任务、成本持久化和 M2 退出评测属于后续切片。

## 私有回滚材料

`edit_file` 和 `write_file` 在提交 `prepared` Journal 时同时提交写前镜像。写前不存在表示该文件由 Agent 创建，回滚动作是删除；写前存在时：

- 不超过 64 KiB 的镜像保存为 Patch Entry 私有 BLOB；
- 更大镜像复用 Artifact Store，以临时文件、fsync、原子 no-clobber link、数据库登记的顺序保存；并发或重复 Prepare 不会覆盖已登记材料；
- Entry 记录大小与 SHA-256，读取时重新校验 Session、Run、kind、private sensitivity、文件类型、大小和摘要；
- 配额不足、材料损坏或登记失败都会在原项目文件副作用之前拒绝写入；
- `session delete --yes` 级联删除 Entry、Artifact 登记和文件。

回滚正文不进入公开 EventStore 投影、EventBus、TUI、JSONL 或模型上下文。

## 授权与执行边界

`rewind_file` 是应用专用 Tool，不在 `DefaultTools` 中注册，Provider 无法调用。执行顺序固定为：

1. Resolve/Inspect 验证 Session、Journal、项目根、路径指纹、写后 SHA-256 与权限位，并加载已校验写前镜像；
2. Prepare 生成绑定 Command、Journal、路径身份、pre/post 哈希与权限位的规范调用和单文件反向 diff；
3. Policy `Reauthorize` 无条件要求新的人工决定，批准后签发一次性 Capability；
4. Execute 消费 Capability，重新 Inspect，检查根锚定前置条件，持久化 `session.rewind` Command 与 Journal 关联；
5. 根锚定原子替换写前镜像，或删除由 Agent 创建且仍严格匹配写后状态的普通文件；
6. 重新验证写前 SHA-256、存在性与权限位，提交 `applied/rewound`。

用户在原写入后修改内容、chmod、更换路径身份或把文件替换为非普通文件时，自动回滚不会覆盖这些变化。

## 幂等与崩溃恢复

每次 rewind 都先登记私有 Command Ledger 记录。相同 Command ID 必须绑定同一 Session、Journal 和项目身份；重复请求只返回已提交状态，绝不再次执行文件操作。

已持久化执行意图后的恢复只观察当前状态：

- 等于写前状态：补记 Command `applied` 和 Journal `rewound`；
- 仍等于写后状态：Command 标记 `interrupted`，释放 Journal，允许使用新 Command 再次显式审批；
- 两者均不匹配：Command `failed`、Journal `conflict`，保持阻断。

未进入文件副作用边界的 accepted Command 只会终结为 rejected、failed 或 interrupted，不会自动继续。

## 验证覆盖

自动化测试覆盖：既有文件恢复、Agent 新建文件删除、缺失 Capability、内容/权限 TOCTOU、inline/Artifact 两类回滚材料、Artifact 篡改、Session 删除、Command ID 去重、写前/写后/冲突三路崩溃判定、非 TTY 拒绝、交互 diff 审批，以及重复命令不重放。

本地门禁结果：

- `go vet ./...`、`go test ./...` 与 `go test -race ./...` 全部通过，共 529 项测试、18 个包；
- `golangci-lint`（含 `liveprovider` build tag）为 0 issue；
- `govulncheck@v1.1.4 ./...` 未发现漏洞；
- Coding baseline 5/5，live-provider 离线 Harness 3/3；
- `CGO_ENABLED=0` 的 Windows amd64、macOS amd64/arm64、Linux amd64 四目标构建全部通过。
