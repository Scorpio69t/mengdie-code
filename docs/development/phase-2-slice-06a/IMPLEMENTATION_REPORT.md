# P2-06A 实施报告：Patch Journal 写入事实与崩溃判定

## 结论

本切片把 `edit_file` 与 `write_file` 从“原子写但跨进程状态未知”提升为可证明的写入状态机。每次项目文件修改必须先提交私有 Patch Journal，再允许创建父目录、暂存文件或替换目标；写后依次记录 `applied` 并按当前文件重新校验 `post_sha256`。Journal 不携带 Capability，也不会投影到公开事件。

P2-06A 不提供 `mengdie rewind`。回滚材料、用户可见冲突处理和安全 rewind 属于 P2-06B，因此 M2 仍保持未完成。

## 持久模型

迁移 `006_patch_journals.sql` 新增 Journal 头和单文件 Entry。头绑定 Session、Run、Command、工具调用 ID、工具名与规范参数摘要；Entry 只保存项目相对路径、规范路径指纹、存在性、pre/post SHA-256、权限模式和状态。完整代码内容不写入 SQLite，正反向 Artifact 引用保留为后续 P2-06B 的受控扩展点。

当前状态转换为：

```text
prepared -> applied -> verified
    |           |
    +-----------+-> conflict
    |
    +-> aborted / verified / conflict  （中断恢复判定）
```

## 写入边界

工具执行顺序固定为：Capability 消费、路径重解析、原前置条件检查、根锚定检查、提交 `prepared`、再次根锚定检查、原子替换、提交 `applied`、读取当前文件并提交 `verified`。Journal 缺失或写入失败时，工具在项目文件副作用之前拒绝执行。

`write_file` 同时拒绝内容完全相同的覆盖，因为相同 pre/post 哈希无法在崩溃后证明替换是否发生。

## 恢复语义

Resume Analyzer 对未完成的 write 工具加载私有调用与 Journal，并重新验证项目根、相对路径、路径指纹和调用摘要：

- 当前状态等于 pre：Journal 标记 `aborted`，只能重新 Prepare、展示当前预览并获取新的单次 Capability；
- 当前状态等于 post：Journal 标记 `verified`，新 Run 只补认已完成结果，不再次执行文件工具；
- 两者均不匹配或路径身份变化：Journal 标记 `conflict`，恢复保持阻断。

execute/network 工具仍保持未知副作用阻断。

## 验证覆盖

自动化测试覆盖 create、overwrite、edit、Journal 缺失、Journal 提交后的第二次 TOCTOU、prepared/applied kill point、pre/post/conflict 判定、外部篡改、项目外路径、Session 级联删除，以及 verified write 恢复不重复执行。

本地门禁结果：

- `go vet ./...`、`go test ./...` 与 `go test -race ./...` 全部通过；
- `golangci-lint run --build-tags=liveprovider ./...` 为 0 issue，包含 errcheck；
- `govulncheck@v1.1.4 ./...` 未发现漏洞；
- `CGO_ENABLED=0` 的 Windows amd64/arm64 与 macOS amd64/arm64 构建全部通过；
- Coding baseline 5/5 通过。
