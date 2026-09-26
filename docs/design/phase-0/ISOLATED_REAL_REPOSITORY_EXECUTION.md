# 真实仓库正式隔离执行：能力门禁与验证方案

> 状态：`mengdie-z61.5.1` 设计切片；截至 2026-09-27 **未实现正式执行环境，也没有正式 M1 真实仓库成绩**。`repo baseline` 和 `repo run` 均保持 `local_unisolated`。
> 上位契约：[真实仓库单任务执行边界](./REAL_REPOSITORY_EXECUTION_BOUNDARY.md)。本页只决定正式隔离执行的入口门禁和验证方法，不改变现有本地诊断。

## 决策

- **用户痛点与指标：** 本地诊断能证明三份固定源码副本、预算、精确 diff 和独立 verifier 的应用层协议，却无法证明子进程不能读宿主凭据、访问任务外文件或直接出网。正式 M1 必须在 macOS 和 Windows 分别给出可复验的隔离证据，且至少各有 5 个外部任务的完整分母。
- **所属层次与最小闭环：** Harness 评测执行环境。先做一个无真实 Provider、无不可信仓库的负向探针：同一客体进程尝试访问任务外诱饵、宿主凭据诱饵、外部网络和取消后的进程；任何一项未被阻断即拒绝进入正式任务。
- **选择方案：** 正式运行以每任务一次性的虚拟机为候选执行主体。macOS 调研 Apple Virtualization.framework；Windows 优先调研可控制内存、磁盘、网卡与销毁的 Hyper-V VM。二者只是实施候选，必须通过下面的实际探针才可启用正式评分。Windows Sandbox 可用作原型，但不能仅凭配置文件推定满足全部门禁。
- **上下文与持久化边界：** 客体只收到已审核任务的公开 prompt、固定源码、明确允许的路径、预算及无秘密工具链。Provider 凭据和原始证据留在受信宿主。模型请求由客体通过一个有界、经验证的请求通道交给宿主 Provider 代理；宿主只返回模型协议所需内容。传输机制需单独安全审查，不能把宿主目录、进程环境、通用网络代理或原始凭据暴露给客体。
- **工具与风险：** 客体内所有文件、Shell、测试及其子进程都被视为不可信；现有 Policy 与调用预算继续生效，但不充当隔离证明。验证在 Agent 客体停止、所有进程清理得到确认后，于另一个一次性客体中执行；沿用已实现的三副本与文件差异重建协议。
- **失败、中断与恢复：** 任一探针失败、执行主体状态未知、模型代理超时、证据缺失或销毁失败，结果只能为 `unsupported`、`preflight_failed` 或 `indeterminate`，不能为正式 `passed`。中断后销毁客体；不在旧客体或旧工作树中自动重跑。新尝试使用新 `run_id`。
- **跨平台与 CI：** 不能用另一平台的探针结果替代本平台结果。普通 CI 只检查协议、拒绝行为和脱敏；具有实际隔离主体的受控主机才执行强制探针和真实任务。GitHub 托管的 macOS runner 不支持嵌套虚拟化，不能在其上假定可以启动本方案的嵌套 VM。[GitHub runner 说明](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)
- **明确不做：** 把同账户环境变量过滤、PathGuard、进程组/Job Object、Windows Sandbox 默认配置或 CI VM 本身直接声明为正式隔离；隐藏测试、任意网络、私有仓库和自动创建公开成绩。

## 平台候选与待证实的前提

| 平台 | 候选主体 | 已有官方能力 | 必须现场核验的缺口 |
|---|---|---|---|
| macOS | 单任务 Apple Virtualization.framework VM | 可显式配置客体的网络、存储、共享目录和内存设备。[Apple Virtualization](https://developer.apple.com/documentation/virtualization) | 可用的预装且可固定镜像、无网卡启动、仅任务数据传输、CPU/内存/磁盘上限、强制停止和彻底销毁；客体与宿主 Provider 通道的安全性。 |
| Windows | 单任务 Hyper-V VM | Hyper-V 提供 VM 内存配置、网卡断开与关闭命令。[内存](https://learn.microsoft.com/en-us/powershell/module/hyper-v/set-vmmemory)、[网卡](https://learn.microsoft.com/en-us/powershell/module/hyper-v/disconnect-vmnetworkadapter)、[关闭](https://learn.microsoft.com/en-us/powershell/module/hyper-v/stop-vm) | 宿主版本/权限、固定镜像与磁盘上限、无网络配置的实时证明、安全数据通道、取消后强制停止及残留检查。 |
| Windows 原型 | Windows Sandbox | 可显式关闭网络、剪贴板和宿主映射，CLI 可启动/停止客体。[配置](https://learn.microsoft.com/en-us/windows/security/application-security/application-isolation/windows-sandbox/windows-sandbox-configure-using-wsb-file)、[CLI](https://learn.microsoft.com/en-us/windows/security/application-security/application-isolation/windows-sandbox/windows-sandbox-cli) | CLI 从 Windows 11 24H2 开始提供，`exec` 目前不能取回进程输出；共享可写目录会持续影响宿主；配置文件的内存值可能被提高到启动下限。尚无本项目所需的完整资源、输出和进程证据链。 |

上述“已有能力”来自供应商文档，**不是本项目的通过证据**。选择 Hyper-V 或 Apple VM 也不能自动证明隔离；客体镜像、挂载、设备和宿主通道都必须核验。GitHub 托管 runner 即使为新 VM，也不能在其中把 Provider key 与不可信子进程共用一个权限主体；[GitHub 的 runner 安全说明](https://docs.github.com/en/actions/concepts/security/compromised-runners)指出工作流中暴露的 secret 可能被恶意命令取得。

## 强制探针及通过标准

探针在**与正式任务同一镜像、同一启动配置、同一数据通道和同一预算实现**上运行；更改上述任一项后重跑。每项记录平台、OS build、镜像及工具链 SHA-256、执行器 commit、启动配置摘要、UTC 时间、客体 ID、退出码、超时、宿主侧观测和脱敏日志哈希。仅记录客体自述不算通过。

| 探针 | 客体内动作 | 宿主侧通过标准 |
|---|---|---|
| 任务外文件/凭据 | 读取两个仅存在宿主的随机诱饵；枚举宿主 HOME、凭据和共享路径；尝试通过允许的数据通道请求诱饵 | 客体无法取得任一诱饵字节；宿主确认只挂载已批准的公开输入和专用输出，代理拒绝非模型协议请求。 |
| 子进程网络 | 在直接子进程、Shell、测试子进程中分别尝试 DNS、TCP 和 HTTP 访问受控目标 | 宿主侧网络观测为零成功连接；失败码及配置记录一致。Provider 请求只能经受限宿主通道完成，不能借客体网络直接出站。 |
| 文件副作用 | 写任务外绝对路径、父目录、符号链接/Windows reparse point；向输出通道提交恶意路径 | 宿主诱饵与任务外目录哈希不变；宿主按初始快照重建精确白名单普通文件，拒绝别名、链接及越界提交。 |
| 取消与残留 | 启动多层子进程和延迟写入/联网动作，然后取消任务并销毁客体 | 宿主确认客体停止，迟发诱饵、网络与任务外文件均未变化；停止/销毁失败记 `indeterminate`。 |
| 硬资源限制 | 申请超预算内存、填满客体磁盘、持续占用 CPU/进程数 | 由执行主体而非应用统计阻断；记录具体限制、观测值与触发方式。某项不能提供硬限制则正式评分保持关闭。 |
| 工具链一致性 | 客体报告预装编译器、运行时、verifier 的二进制哈希与版本；基线和终态重复核对 | 与已审核镜像/任务定义一致；缺失依赖时预检失败，运行期不下载依赖。 |

预检要求**所有**探针通过；结果证据必须包括未运行、失败和未知项的分母。探针正常通过后仍需先用固定的公开任务做一个完整“基线 → Agent → 停止 → 重建 → 独立验证 → 销毁”演练，证明状态机和传输没有跨主体漏洞。之后再执行每平台至少 5 个已审核外部任务，并单独核对至少 3 个读改测修正循环。

## 后续实现顺序

1. 固定一个平台的执行器/客体镜像和最小宿主消息通道；消息仅支持有界模型请求/响应和受审计的产物交换，带 `run_id`、大小上限、超时和严格解析。宿主永不执行客体提交的路径或命令。
2. 在正式 runner 中添加执行环境预检和显式正式模式。只有真实执行器返回完整、可信的探针与生命周期证据，才能进入正式状态机；现有 `repo run` 始终输出 `local_unisolated`。
3. 在第二平台复制同一协议并运行其本地探针。记录平台特有的失败与拒绝，不把协议测试伪装成 OS 隔离测试。
4. 审核候选仓库的许可证、固定 commit、基线可复现性和离线依赖；运行真实任务，发布脱敏的分子、分母及失败项。

本设计未引入依赖或公共接口。正式实现前须按[依赖准则](../../DEPENDENCIES.md)复核虚拟化封装、镜像来源和供应链成本。
