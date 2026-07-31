# Notes：第一阶段 Slice 04

## 输入约束

- 用户已授权在 `agent/phase1-slice04` 分支上继续 P1-04，品牌图标改动随本分支一起稍后 PR。
- 设计基线：`docs/design/phase-1/DETAILED_DESIGN.md` §10（工具系统）、§11.3（Capability 绑定字段）、§12.3（双平台路径重点）、§16.3（关键安全测试）与 §17 P1-04 验收标准。
- macOS、Windows 为 Tier 1；负向安全测试必须覆盖路径逃逸与旁路尝试。

## Deep Agent 门禁映射

- Safety：PathGuard 是文件工具平面的确定性边界；路径规范化先于包含性判断；敏感路径、`.git` 写入、项目根外路径默认拒绝。
- Context：PreparedCall 的 Preview 与 CanonicalArg 为审批展示提供有界信息，不承担权限职责。
- Recovery：Precondition（文件哈希）使"批准后内容变化"可检测并安全失败，是 M2 rewind 的演进点。
- Evaluation：负向测试覆盖逃逸、前缀绕过、symlink、新文件祖先逃逸、Windows 设备/ADS/UNC、大小写语义。

## Findings

### 设计约束摘录（DETAILED_DESIGN.md）

- §10.1：Prepare 不产生外部副作用；Approval 绑定 Digest；Execute 前重新检查路径、文件哈希与参数摘要。
- §10.2：项目根顺序 `--cwd` > 向上 `.git` > 当前目录（提示风险）；读取或写入前解析 symlink/junction/reparse point；新文件解析最近已存在父目录；默认禁止写 `.git`、配置目录、凭据文件和项目根外路径；Windows 拒绝设备路径、NT namespace、ADS 与未批准 UNC；macOS 不假设大小写不敏感；路径判断使用平台语义。
- §11.3：Capability 绑定 RunID、ToolName、digest、cwd 与路径集合、effect、过期时间、单次 nonce。
- §16.3：`project` 与 `project-evil` 前缀绕过、symlink/junction 指向项目外、新文件父目录逃逸、Windows `file.txt:secret` ADS、`.git/config` 写入、批准后文件变化、Capability 重放。

### 仓库审计

- `internal/project.FindRoot` 已实现"向上找 `.git`，找不到返回起始目录"，PathGuard 直接复用，风险提示由调用方负责。
- `internal/events`、`internal/provider` 与本切片无依赖；tools 协议不引用 UI 与 Provider。
- 当前仅 `go-toml` 一个第三方依赖；PathGuard 与 tools 协议使用标准库即可。

### 平台语义核验（2026-07-31）

- Windows 保留设备名：CON、PRN、AUX、NUL、COM1–COM9、LPT1–LPT9，按 basename（去扩展名）匹配，大小写不敏感。
- `\\?\` 与 `\\.\` 前缀绕过常规规范化，必须在判定前显式拒绝或剥离；本切片选择拒绝。
- ADS 语法为 `filename:stream`；盘符之后的第二个冒号即视为 ADS 并拒绝。
- UNC `\\server\share` 默认拒绝，后续如需放行必须显式授权并记录目标。
- `filepath.EvalSymlinks` 在 Windows 上会解析 junction 与 reparse point；创建 symlink 的测试在权限不足时跳过，不伪造通过。
