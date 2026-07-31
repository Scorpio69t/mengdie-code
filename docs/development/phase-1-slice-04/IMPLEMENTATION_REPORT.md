# 第一阶段 Slice 04 实现报告

> 状态：本地验证通过，待评审与远端 CI

## 目标

交付 P1-04 PathGuard 与 Tool 基础协议：Prepare/Execute 边界、effect 声明、参数 digest、前置条件、项目根边界与跨平台路径语义。不实现具体工具、Policy、Approval 或 shell 适配。

## 交付

- 新增 `internal/platform`：
  - `PathGuard`：项目根规范化与一次性符号链接解析；`Resolve(path, mode)` 输出规范化绝对路径与敏感标记；
  - 相对路径锚定项目根；已存在路径全量解析 symlink/junction/reparse point；新文件解析最近已存在祖先后重新拼接；
  - 包含性判断使用 `filepath.Rel` + 平台大小写语义（Windows 折叠），禁止字符串前缀比较，覆盖 `project-evil` 旁路；
  - 硬拒绝：项目根外路径、`.git`/`.mengdie`/`.ssh`/`.aws`/`.gnupg` 与凭据文件（`.env*`、`*.pem`、`*.key`、`id_*`、`.netrc` 等）的写入；
  - 敏感路径读取放行但标记 `Sensitive`，交给 P1-06 Policy 决定 ask/deny；
  - Windows 语义与宿主 OS 解耦（flavor 参数化）：拒绝 `\\?\`、`\\.\`、UNC、盘符相对路径、ADS（`file.txt:secret`）和保留设备名（CON/NUL/COM1–9/LPT1–9 等），任一平台均可测试；
  - 稳定哨兵错误：`ErrOutsideRoot`、`ErrProtectedWrite`、`ErrUNCPath`、`ErrDevicePath`、`ErrADS`、`ErrDriveRelative`、`ErrEmptyPath`。
- 新增 `internal/tools`：
  - `Tool` 两阶段协议：`Prepare`（无副作用，产出规范化参数、Preview、前置条件与 digest）与 `Execute`（携带一次性 Capability）；
  - `Effect`（read/write/execute/network）、`ToolSpec`、`PreparedCall.Validate()`；
  - `Canonicalize`（key 排序、空白规范化）与 `ComputeDigest`（SHA-256，绑定工具名 + 规范化参数，NUL 分隔防歧义）；
  - `PrepareCall` 收尾助手，保证没有工具能漏掉 digest 绑定；
  - `Precondition`（`file_sha256`）与 `CheckPreconditions`：批准后文件变化或删除时安全失败（TOCTOU），返回 `*PreconditionError`；
  - `Capability` 只定义 §11.3 的绑定字段（RunID/ToolName/Digest/WorkDir/Paths/Effects/ExpiresAt/Nonce），签发与校验留给 P1-06；
  - `Registry`：构造期拒绝重名、空名、无 effect、非法 schema，`Specs()` 稳定排序。
- 无新增第三方依赖；`internal/tools` 不依赖 UI、Provider 或 events。

## 验证

本地验证（Windows，Go 1.26.5，`CGO_ENABLED=1` race）：

- `go test ./...`：全部通过（含 platform 13 项、tools 8 项新测试）；
- `go test -race ./internal/platform/ ./internal/tools/`：通过；
- `go vet ./...`：通过；
- `gofmt -l .` 与 `git diff --check`：无输出；
- `mengdie-eval --manifest evals/coding/smoke.json`：5/5 baseline 通过；
- 负向覆盖：`..` 逃逸、多层 `..`、绝对路径越界、`project-evil` 前缀旁路、新文件祖先 symlink 逃逸、经 symlink 读/写逃逸、`.git` 写入、`.env`/`*.pem`/`.mengdie` 保护、Windows `\\?\`/`\\.\`/UNC/盘符相对/ADS/保留设备名、大小写折叠包含性、批准后内容变化与文件删除（TOCTOU）、digest 名称/参数绑定、registry 非法注册。

## 明确不做

- 具体工具实现（P1-05 只读、P1-07 修改、P1-08 shell）；
- Policy 规则合并、Approval 交互、Capability 签发与校验（P1-06）；
- 进程组/Job Object、终端适配（P1-08）；
- 将 tools 协议接入 Agent loop（P1-09）。

## 已知限制

- macOS 大小写语义按"不假设不敏感"处理（区分大小写比较），大小写不敏感卷上的极端混淆路径留给平台 smoke；
- symlink 测试在无法创建符号链接的环境跳过（Windows 无开发者模式时），GitHub Windows runner 与本机均可真实执行；
- `Canonicalize` 保持数字字面量形式（`1e2` 与 `100` 不互认），模型参数按字节一致处理，语义化数字规范化留待真实需要时评估。
