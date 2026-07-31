# Task Plan：第一阶段 Slice 04

## Goal

交付 P1-04 PathGuard 与 Tool 基础协议：Prepare/Execute 边界、effect 声明、参数 digest、前置条件、项目根边界和跨平台路径语义，为只读/修改工具与 Policy 提供确定性安全地基。

## Phases

- [x] Phase 1：合并 P1-03 与品牌图标改动，冻结 P1-04 范围
- [x] Phase 2：实现 `internal/platform` PathGuard（规范化、符号链接、项目边界、敏感路径、Windows 语义）
- [x] Phase 3：实现 `internal/tools` 协议（Tool/ToolSpec/Effect/PreparedCall/Preview/Precondition/digest/Registry）
- [x] Phase 4：完成路径逃逸、前缀绕过、TOCTOU、Windows 设备/ADS/UNC 等负向测试
- [x] Phase 5：运行完整验证（fmt/vet/test/eval），更新文档并提交

## Key Questions

1. 路径规范化与符号链接解析如何保证最终路径仍在项目根内，且不被 `project-evil` 式前缀绕过？
2. Windows 设备路径、保留名、ADS、UNC、盘符大小写如何在不依赖宿主 OS 的前提下测试？
3. Prepare 与 Execute 的边界如何防止"批准后内容被替换"？
4. digest 与 Capability 需要为 P1-06/P1-07 预留到什么程度的形状，而不提前实现它们？

## Decisions Made

- 本切片只交付 P1-04：不实现具体工具（P1-05/P1-07）、Policy/Approval/Capability 签发（P1-06）、shell 进程适配（P1-08）。
- PathGuard 放在 `internal/platform`，路径校验逻辑与宿主 OS 解耦（flavor 参数化），保证 macOS/Windows 语义都可在任一开发机上测试。
- 符号链接解析使用 `filepath.EvalSymlinks`；新文件解析最近已存在祖先后重新拼接。
- 包含性判断使用 `filepath.Rel` 加平台大小写语义，禁止字符串前缀比较。
- `Capability` 只定义为绑定字段的令牌类型，签发与校验属于 P1-06。
- 不新增第三方依赖。

## Errors Encountered

- `checkWindowsComponents` 初版使用 `filepath.VolumeName`，在 Unix 宿主上无法识别 `C:` 盘符，会把 `C:` 误判为 ADS 组件；改为手工剥离盘符，保证 Windows 语义测试跨宿主一致。

## Status

**Completed locally** — 本地 fmt/vet/test/race/eval 全部通过，等待评审、推送与远端三平台 CI。
