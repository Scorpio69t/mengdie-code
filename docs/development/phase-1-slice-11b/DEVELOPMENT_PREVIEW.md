# MengDie Code 开发预览

> 状态：P1-11B，unsigned 开发预览  
> 目标：让 macOS 与 Windows 用户在正式发布渠道建立前验证真实 CLI 闭环

## 产物与触发方式

GitHub Actions 的“开发预览”工作流在 Pull Request、`main` push 和手动触发时运行。每次运行生成独立的 `0.1.0-dev.<run_number>` 版本，并注入完整 commit SHA 与 UTC 构建时间。

| 目标 | 文件 | 支持等级 |
|---|---|---|
| macOS Apple Silicon | `mengdie-<version>-darwin-arm64.tar.gz` | Tier 1 |
| macOS Intel | `mengdie-<version>-darwin-amd64.tar.gz` | Tier 1 best effort |
| Windows x64 | `mengdie-<version>-windows-amd64.zip` | Tier 1 |
| Linux x64 | `mengdie-<version>-linux-amd64.tar.gz` | Tier 2 |

每个 GitHub Artifact 包含归档、归档的 `.sha256` 和 Go build info。归档内部包含 CLI 与 `metadata.json`。Artifact 只保留 7 天，不是正式 Release。

## 下载与校验

1. 打开仓库 Actions 中成功的“开发预览”运行。
2. 在 Artifacts 区选择与平台一致的目标。
3. 解压 GitHub 下载的外层压缩包。
4. 在运行程序前校验归档 SHA-256。

macOS / Linux：

```bash
shasum -a 256 -c mengdie-*.sha256
```

Windows PowerShell：

```powershell
$archive = Get-ChildItem mengdie-*.zip | Select-Object -First 1
$expected = (Get-Content "$($archive.FullName).sha256").Split()[0].ToLowerInvariant()
$actual = (Get-FileHash -Algorithm SHA256 $archive.FullName).Hash.ToLowerInvariant()
if ($actual -ne $expected) { throw "SHA-256 校验失败" }
```

checksum 与二进制来自同一次 workflow，可发现下载损坏，但不能替代签名和独立来源验证。高风险环境应从已审核 commit 自行构建。

## 安装与卸载

macOS 解开 `.tar.gz` 后可以安装到用户路径：

```bash
mkdir -p "$HOME/.local/bin"
install -m 0755 mengdie "$HOME/.local/bin/mengdie"
"$HOME/.local/bin/mengdie" version
"$HOME/.local/bin/mengdie" doctor --offline
```

卸载时删除 `$HOME/.local/bin/mengdie`。未签名预览没有 Apple notarization；macOS 可能要求用户在系统安全界面明确批准。不要关闭全局系统安全策略。

Windows 解开 `.zip` 后，将 `mengdie.exe` 放入用户拥有的目录并把该目录加入用户级 `PATH`：

```powershell
$target = Join-Path $env:LOCALAPPDATA "MengDie\bin"
New-Item -ItemType Directory -Force $target | Out-Null
Copy-Item .\mengdie.exe $target
& "$target\mengdie.exe" version
& "$target\mengdie.exe" doctor --offline
```

卸载时删除该目录中的 `mengdie.exe`，并按需移除用户级 `PATH` 条目。预览没有 Windows Authenticode 签名，SmartScreen 可能显示未知发布者；不要禁用全局 SmartScreen。

## 构建与验证边界

- 四目标产物均使用 Go 1.26.5、`CGO_ENABLED=0`、`-trimpath` 与显式版本元数据构建。
- macOS、Windows、Ubuntu 原生 runner 会分别构建当前平台二进制，并真实运行 `version` 和 `doctor --offline --json`。
- 交叉编译任务只证明产物可生成和元数据可检查，不冒充真实设备执行证据。
- 预览不包含 Homebrew tap、Winget、自动更新、签名、公证、SBOM 或二进制来源证明；这些属于正式发布设计。
- CLI 仍是单进程单任务且退出即失去上下文；EventStore、resume 与完整 TUI 属于 M2。

## 供应链选择

本切片没有新增 Go 依赖。工作流仅使用 GitHub 官方 `actions/checkout@v7`、`actions/setup-go@v7` 与 `actions/upload-artifact@v7`；2026-08-06 已通过各官方仓库最新 release 核验主版本。正式发布前仍需决定是否把 Action 固定到完整 commit SHA、增加签名和 provenance。
