# Notes：第一阶段 Slice 05

## 输入约束

- 设计基线：DETAILED_DESIGN.md §10.3（只读工具规格）、§9.3（M1 预算：单文件 32 KiB、tool output 64 KiB 保留头尾）、§16.3（安全测试）、§17 P1-05 验收（大型目录、Unicode 路径、忽略规则、截断）。
- P1-04 已合并：`platform.PathGuard`、`tools` 协议（Prepare/Execute、digest、precondition、Capability 类型）。

## Deep Agent 门禁映射

- Context：文件读取 32 KiB、搜索输出 64 KiB、匹配行 500 字符；大输出截断并明确标注，不静默溢出上下文。
- Safety：路径两次过 guard（Prepare + Execute）；二进制不注入；rg 以固定 argv、无 shell、`--` 分隔调用；搜索路径限定项目根内。
- Recovery：`read_file` 的 `file_sha256` 前置条件使批准后修改可检测；capability 最小校验（名称 + digest）提前建立不变量。
- Evaluation：逃逸、capability 缺失/错配、TOCTOU、Unicode 文件名、大目录 limit、截断标记均有负向/边界测试。

## Findings

### 设计约束摘录

- `read_file`：输入 path、可选 start_line/end_line；输出带行号文本、编码、截断信息与内容 SHA-256；二进制返回类型和大小。
- `list_files`：输入 path、glob、max_depth、limit；输出相对项目根稳定排序路径；默认忽略 `.git`、构建目录；达到 limit 明确标注。
- `search_text`：输入 query、path、glob、case_sensitive、limit；优先 `rg`，退化纯 Go walker；结果按 path + line 排序，限制单行和总输出。

### 实现事实

- rg 输出 `path:line:text`：Windows 绝对路径含盘符冒号，无法按冒号切分；改为 cwd=搜索根、传 `.`，使输出路径为相对形式，解析后与项目根相对位置拼接。
- rg 退出码 0=有匹配、1=无匹配、2=错误；1 不是失败。
- 本机已有 rg 14.1.0（chocolatey），双引擎等价性可本地验证；CI 三平台同样有 rg 预装。
- `ExecEnv` 在 P1-04 只有时钟；本切片加入 `Guard`，与 PrepareEnv 对齐"Execute 前重新检查路径"。
