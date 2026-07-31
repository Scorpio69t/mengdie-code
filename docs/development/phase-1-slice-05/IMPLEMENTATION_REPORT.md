# 第一阶段 Slice 05 实现报告

> 状态：本地验证通过，待评审与远端 CI

## 目标

交付 P1-05 只读工具：`read_file`、`list_files`、`search_text`（rg 优先、纯 Go fallback），接入 P1-04 协议并落实输出限制。不实现修改工具、shell、Policy/Approval。

## 交付

- 协议补强（`internal/tools`）：
  - `PrepareEnv.CallID`：Agent loop 分配的调用 ID 进入 `PreparedCall.ID`；
  - `ExecEnv.Guard`：Execute 在批准后重新解析路径，目标不可被偷换到根外；
  - `CheckCapability`：协议级最小不变量——capability 必须存在且绑定同一 ToolName + Digest（`ErrCapabilityMissing`/`ErrCapabilityMismatch`）；签发、过期与 nonce 消费属 P1-06；
  - 共享输出预算：单文件 32 KiB、工具输出 64 KiB（UTF-8 安全截断 + 标注）、匹配行 500 字符；
  - `decodeArgs`：严格解码，未知字段直接报错。
- `read_file`：行号 + 行范围（1 起始含端点）；UTF-8/二进制检测（NUL 或非法 UTF-8 采样），二进制只返回类型与大小；超长行截断；内容 SHA-256 前置条件，批准后修改安全失败；敏感文件在 Preview 标注。
- `list_files`：相对项目根、稳定排序；M1 默认忽略 `.` 隐藏项与 `node_modules/dist/build/target/out/bin/obj/vendor`；不跟随符号链接文件；`max_depth`、`limit`（默认 100、上限 1000）与 glob（basename 或含 `/` 的相对路径）；超限明确标注。
- `search_text`：字面量搜索；优先系统 `rg`（固定 argv、无 shell、`--regexp`+`--`、30 秒超时、cwd=搜索根使输出路径无盘符冒号），rg 缺失时退化纯 Go walker；M1 忽略集通过 `--glob !name/**` 显式传给 rg（rg 只在 git 仓库内应用 gitignore）；大小写默认敏感，`case_sensitive=false` 双引擎语义一致；结果按 path:line 排序，相对项目根显示；limit（默认 50、上限 500）+ 64 KiB 总量截断。
- `DefaultTools()` 稳定顺序返回三工具，注册在应用边界进行。
- 无新增第三方依赖。

## 验证

本地验证（Windows，Go 1.26.5，rg 14.1.0）：

- `go test ./...`：13 包全部通过（tools 新增 16 项测试）；
- `go test -race ./internal/tools/`：通过；
- `go vet ./...`：通过；`gofmt -l .`、`git diff --check`：无输出；
- `mengdie-eval --manifest evals/coding/smoke.json`：5/5 baseline 通过；
- 覆盖：行范围边界、32 KiB 截断标注、二进制不注入、中文内容与中文文件名、大目录 limit、忽略规则（`.git`/隐藏项/构建目录/依赖目录）、glob 两种形式、rg 与 fallback 结果等价、大小写两种模式、长行 500 字符、排序稳定性、路径逃逸、目录当文件/文件当目录、未知字段、capability 缺失与错配、批准后内容变化（TOCTOU）。

## 明确不做

- `edit_file`/`write_file`（P1-07）、`shell`（P1-08）、`write_todos`（P1-09）；
- Policy 规则、Approval 交互与 Capability 签发（P1-06）；
- 将工具接入 Agent loop 与 CLI（P1-09）；
- 可配置 ignore 规则（M1 只交付默认集，设计中的"配置的 ignore"随配置装配后补）。

## 已知限制

- 大小写不敏感折叠使用 `strings.ToLower`（Unicode 简单折叠），与 rg 的 `--ignore-case` 在罕见字符上可能有个案差异；
- rg 结果解析依赖"cwd 相对路径无冒号"，若 rg 未来改变输出格式，契约测试会失败而不是静默错解析；
- 符号链接文件不跟随（列出但不可搜索/遍历），真实读取时由 guard 再解析。
