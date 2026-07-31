# Task Plan：第一阶段 Slice 05

## Goal

交付 P1-05 只读工具：`read_file`、`list_files`、`search_text`（rg 优先、纯 Go fallback），接入 P1-04 协议（Prepare/Execute、guard、digest、capability），并落实输出限制。

## Phases

- [x] Phase 1：从 main 切出分支，冻结 P1-05 范围
- [x] Phase 2：协议补强（ExecEnv.Guard、CheckCapability 最小校验）
- [x] Phase 3：实现 `read_file`（行范围、行号、二进制检测、32 KiB 截断、哈希前置条件）
- [x] Phase 4：实现 `list_files`（忽略规则、深度、limit、稳定排序）
- [x] Phase 5：实现 `search_text`（rg 子进程 + 纯 Go walker、排序、行宽与总量截断）
- [x] Phase 6：负向与边界测试（大目录、Unicode 路径、逃逸、capability 缺失/错配、TOCTOU）
- [x] Phase 7：完整验证（fmt/vet/test/race/eval）与文档

## Key Questions

1. Execute 如何在不依赖 P1-06 的前提下验证 capability 的最小不变量（ToolName + Digest 绑定）？
2. rg 输出在 Windows 盘符含冒号时如何稳定解析？（cwd 相对路径方案）
3. 二进制与超大文件如何不进入上下文？
4. 忽略规则的 M1 默认集是什么？（隐藏项 + 常见构建目录）

## Decisions Made

- 本切片只交付三个只读工具；edit/write（P1-07）、shell（P1-08）、Policy/Approval（P1-06）不实现。
- 所有路径在 Prepare 与 Execute 两次经过 PathGuard；`read_file` 携带 `file_sha256` 前置条件。
- `search_text` 为字面量搜索（rg `-F`）；默认大小写敏感，`case_sensitive=false` 时两边引擎语义一致（Unicode 简单折叠）。
- rg 缺失时自动退化纯 Go walker；测试可强制 fallback 以验证等价性。
- 输出上限：单文件读取 32 KiB、工具输出 64 KiB、匹配行 500 字符（§9.3）。
- 忽略规则 M1 默认：`.` 开头隐藏项 + `node_modules/dist/build/target/out/bin/obj`。
- 不新增第三方依赖。

## Errors Encountered

- rg 只在 git 仓库内应用 gitignore，临时项目中的 `node_modules` 会被搜索；改为把 M1 忽略集以 `--glob !name/**` 显式传入。
- rg 在 Windows 输出 `.\a.go` 形式路径，原先只处理 `./` 前缀导致与 fallback 输出不一致；统一先 `filepath.ToSlash` 再剥离 `./`。
- `list_files` 截断测试按行数统计时被末尾空行干扰；改为裁剪后计数。

## Status

**Completed locally** — 本地 fmt/vet/test/race/eval 全部通过，等待评审、推送与远端三平台 CI。
