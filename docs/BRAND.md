# MengDie Code 品牌与启动体验

## 1. 品牌核心

- 中文名：梦蝶 Code
- 英文名：MengDie Code
- 命令：`mengdie`
- 标语：不是记得更多，而是记得更对。

标志是一只由代码尖括号几何结构组成的蝴蝶：蝴蝶对应“梦蝶”与复盘，尖括号对应 Coding，中央竖形同时暗示编辑器光标与可追溯的证据链。

## 2. CLI 启动画面

交互式启动时展示：

```text
  ╭╲      ╱╮
  │ ╲    ╱ │
  ╰╮ ╲  ╱ ╭╯
    ╲ ╲╱ ╱
    ╱ ╱╲ ╲
  ╭╯ ╱  ╲ ╰╮
  │ ╱    ╲ │
  ╰╱      ╲╯

MengDie Code / 梦蝶 Code  0.0.0-dev
不是记得更多，而是记得更对。

  构建  <commit> · <build date>
  平台  <os/arch> · <go version>
  项目  <working directory>
  模型  <provider:model 或 未配置>
  安全  <当前真实执行等级>
```

约束：

- Banner 只属于交互式入口；`version`、`exec --json`、管道和 CI 输出不显示 Logo。
- 模型、安全等级和功能状态必须显示真实值，禁止为了视觉完整伪造“已连接”或“已沙箱化”。
- M1 不为 Banner 引入 TUI 框架或 ANSI 依赖；颜色由后续 renderer 根据 TTY、`NO_COLOR` 和主题决定。
- 紧凑终端可以折叠 Logo，但版本、模型、目录和安全状态仍可查看。

## 3. Web 使用

- Web 默认使用 `assets/brand/mengdie-mark.svg`，PNG 作为兼容和衍生资源。
- 产品名使用 HTML 真实文本，不烘焙进图片，以支持中文优先、英文切换、搜索和无障碍访问。
- 图片替代文本使用“由代码尖括号构成的梦蝶 Code 蝴蝶标志”。
- 后续 Web 主题只调整容器背景和排版，保持 Logo 几何与主色一致。

## 4. 构建信息注入

发布构建通过 `-ldflags -X` 注入：

```text
main.version
main.commit
main.buildDate
```

本地开发保留 `0.0.0-dev / unknown / unknown`，让用户可以一眼区分正式产物和开发构建。
