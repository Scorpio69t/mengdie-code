# M3 Slice 01 — 可信记忆 SQLite schema + FTS5 + 显式 CLI + Agent 集成 设计

> 状态：已通过 6 节分段确认，待用户 review spec 后转入 writing-plans。
> 日期：2026-08-24
> 关联 Beads：`mengdie-n47`
> 关联架构：`ARCHITECTURE.md §8（可信记忆系统）`、`§15（M3 验收）`

## 1. 背景

M2（值得信任）已通过 P2-08B 退出评测：EventStore、Command Ledger、Session Resume、Patch Journal、Artifact Store、Context Summary、Skill、TUI 事实订阅、用量与成本估算均已落地，并已由 `cmd/mengdie-eval chaos` 模拟与真实 Provider 验证。

M3 的产品承诺（ARCHITECTURE §1.3「记得对」）要求：

> 记忆有来源、作用域、权威等级和有效期；被召回不等于被证实；错误记忆可以追踪、纠正、失效和删除。

ARCHITECTURE §15 列出 M3 交付：
- 可信记忆 schema
- 显式 remember / forget / show / why / list / supersede / approve / rebuild / export
- 任务结束候选提取
- FTS5 + 结构化召回
- 冲突、过期与来源审计
- Memory Trust Set 回归

本切片交付前四项，第五项的最小可见部分（why 展示冲突链），第六项作为退出门禁。任务结束候选提取（自动 inferred → proposed）保留为 M3 Slice 02 的入口（本切片定义 `MemoryExtractor` 接口与占位实现）。

## 2. 范围与不在范围

### 2.1 范围内

- internal/memory: SQLite schema（memories + memory_evidence + memory_usage 三张表 + FTS5 external content 虚拟表 + 同步触发器）+ 008_memory.sql 迁移
- internal/memory/store: `Save`（按 Authority 路由到 `SaveUserMemory` / `SaveRepositoryFact` / `SaveVerifiedFact` / `ProposeMemory`）+ `List` / `Get` / `Why` / `Forget` / `Supersede` / `Approve` / `RecordEvidence` / `RecordUsage` / `RecomputeEvidenceScore`；Authority 写入守门 + Conflict 状态机
- internal/memory/retrieve: 三级召回实现（常驻能力说明、任务级主题目录、原子记忆正文）+ 评分公式
- internal/tools/memory_recall.go: 暴露给 Agent 的按需召回工具（effect=state）
- internal/app/memory.go: `mengdie memory list/show/why/remember/forget/supersede/approve/rebuild/export` 9 个子命令
- agent.Options: 新增 `MemoryRetriever` 接口与 `ProjectIdentity` 字段；`Agent.Run` 第一个 turn 前召回
- evals/memory/trust-set-v1.json: 30 个场景（explicit 15 / repository 5 / verified 5 / inferred 5）
- evals/memory/runner.go: Trust Set 评测 runner + JSON evidence 输出
- liveprovider 端到端测试 + CI 集成
- docs/development/phase-3-slice-01/IMPLEMENTATION_REPORT.md

### 2.2 不在范围内（明确写出避免范围漂移）

- 任务结束自动候选提取（`MemoryExtractor` 接口留空实现）
- embedding / 向量检索（v0.1 仅 FTS5；ARCHITECTURE §8.7 暂缓）
- Reflect / Consolidate / 复盘报告（属于 M4 切片）
- 跨项目记忆复制 / 同步
- 任何对 session 现有 schema 的改动（新增迁移而非修改）
- daemon / 多客户端共享记忆（M5 范畴）

## 3. 存储与 schema（Section 1 已确认）

**新增迁移 `internal/session/migrations/008_memory.sql`**，与 session 共享同一个 SQLite 文件 `state.db`。通过 `embed.FS` 走与现有 7 个迁移相同的 checksum 校验与单链表。

```sql
CREATE TABLE memories (
    rowid          INTEGER PRIMARY KEY,
    id             TEXT NOT NULL UNIQUE,
    claim          TEXT NOT NULL,
    kind           TEXT NOT NULL,        -- episode/fact/preference/procedure/reference
    scope_kind     TEXT NOT NULL,        -- user/project/branch/task
    scope_value    TEXT,
    authority      TEXT NOT NULL,        -- explicit/repository/verified/inferred
    source_type    TEXT NOT NULL,        -- user_message/agent_message/session_event/file/command_result
    source_ref     TEXT NOT NULL,        -- 路径 / 事件 ID / 命令摘要等
    observed_at    DATETIME NOT NULL,
    valid_from     DATETIME,
    valid_until    DATETIME,
    status         TEXT NOT NULL,        -- proposed/active/stale/disputed/superseded/archived
    confidence     REAL NOT NULL,
    evidence_score REAL NOT NULL DEFAULT 0,
    supersedes     TEXT,
    created_at     DATETIME NOT NULL,
    updated_at     DATETIME NOT NULL
);

CREATE INDEX idx_memories_scope     ON memories(scope_kind, scope_value, status);
CREATE INDEX idx_memories_authority ON memories(authority, status);
CREATE INDEX idx_memories_validity  ON memories(valid_until) WHERE valid_until IS NOT NULL;

CREATE VIRTUAL TABLE memories_fts USING fts5(
    claim,
    content='memories',
    content_rowid='rowid'
);

CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, claim) VALUES (new.rowid, new.claim);
END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, claim) VALUES('delete', old.rowid, old.claim);
END;
CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, claim) VALUES('delete', old.rowid, old.claim);
    INSERT INTO memories_fts(rowid, claim) VALUES (new.rowid, new.claim);
END;

CREATE TABLE memory_evidence (
    id          TEXT PRIMARY KEY,
    memory_id   TEXT NOT NULL,
    kind        TEXT NOT NULL,           -- user_confirmed/reobserved/task_verified
    source_ref  TEXT NOT NULL,
    weight      REAL NOT NULL,
    created_at  DATETIME NOT NULL,
    FOREIGN KEY(memory_id) REFERENCES memories(id)
);

CREATE INDEX idx_evidence_memory ON memory_evidence(memory_id, created_at);

CREATE TABLE memory_usage (
    memory_id   TEXT NOT NULL,
    session_id  TEXT NOT NULL,
    recalled_at DATETIME NOT NULL,
    outcome     TEXT,                     -- unknown/helpful/harmful/unused
    PRIMARY KEY(memory_id, session_id, recalled_at),
    FOREIGN KEY(memory_id) REFERENCES memories(id)
);

CREATE INDEX idx_usage_recency ON memory_usage(session_id, recalled_at);
```

`PRAGMA memories_fts('rebuild');` 由 `memory rebuild` 与 `doctor` 子命令调用，用于强制重建索引（应对 schema 漂移）。

`internal/memory` 包接收一个 `*session.SQLiteStore`（通过 `OpenMemory(store)` 工厂），不直接打开数据库，避免包循环与重复 migration 表。

## 4. Authority 守门与冲突策略（Section 2 已确认）

### 4.1 Authority 写入守门

| Authority | 谁能写 | 必须带的 source_type | status 初值 | 走哪条 code path |
|---|---|---|---|---|
| `explicit` | 用户的 `mengdie memory remember` 或对话里带 `/remember` 意图 | `user_message` | `active` | `Store.SaveUserMemory` |
| `repository` | 仓库内可验证事实 | `file`（含行号） | `active` | `Store.SaveRepositoryFact` |
| `verified` | 已通过的 test / build / lint 结果 | `command_result`（含退出码） | `active` | `Store.SaveVerifiedFact` |
| `inferred` | 模型在 session 末候选提取 | `agent_message` | `proposed` | `Store.ProposeMemory`（需 `memory approve <id>` 才升 active） |

`Store.Save` 接收 `Memory` 结构体，但内部根据 `authority` 路由到不同方法，对外仍提供单入口。LLM 写入只走 `ProposeMemory`，不允许直接 `active`（守门由 SQL 约束 + Go runtime 双重保证）。

### 4.2 冲突策略

| 场景 | 行为 | `memory why <id>` 额外展示 |
|---|---|---|
| 同一 Scope 下 claim 字符串完全相同（`strings.EqualFold` 大小写不敏感、Unicode 规范化 NFD/NFC 后） | 不创建新行，返回已存在 id（idempotent） | 无 |
| claim 字符串不同 + scope 重叠 + Authority 相同 | 双方都置 `disputed`，不互相覆盖 | 对方 claim + 双方 source |
| claim 字符串不同 + scope 重叠 + Authority 不同 | 双方都置 `disputed`，`inferred` 一方永远不覆盖 `explicit` | 对方 claim + 双方 source + Authority 等级差 |
| `memory supersede <old> <new>` 手动 | `old.status=superseded, old.supersedes=new.id` | 旧 / 新 claim + 手动时间 |
| `valid_until` 到期 | 后台触发器 / 评测时点检查 → `status=stale` | 无 |

### 4.3 evidence_score 算法（v0.1 简单版）

```text
evidence_score = 0
+ 1.0 * count(memory_evidence WHERE kind='user_confirmed')
+ 0.6 * count(memory_evidence WHERE kind='reobserved')
+ 0.3 * count(memory_evidence WHERE kind='task_verified')
```

`evidence_score` 每次新增 / 删除 evidence 行后由 `Store.RecomputeEvidenceScore(memoryID)` 重算。LLM 写入时填的 `confidence` 字段是另一回事，与 `evidence_score` 独立。

## 5. CLI 表面（Section 3 已确认）

子命令树（在 `cmd/mengdie/memory.go` 注册）：

```text
mengdie memory list
    [--scope user|project|branch|task]
    [--authority explicit|repository|verified|inferred]
    [--status proposed|active|stale|disputed|superseded|archived]
    [--limit 20] (上限 200)
    [--json]
mengdie memory show <id>
mengdie memory why <id>
mengdie memory remember <claim>
    [--scope project]                              (默认 project)
    [--authority explicit]                          (默认 explicit)
    [--kind fact]                                   (默认 fact)
    [--valid-until 30d]                             (可选)
    [--source "session:42:user"]                   (可选，自动从 env 推)
mengdie memory forget <id>
    [--hard]                                        (默认 archive，--hard 真删)
mengdie memory supersede <old-id> <new-id>
mengdie memory approve <id>                          (仅 status=proposed)
mengdie memory rebuild                              (FTS5 索引重建)
mengdie memory export
    [--scope ...] [--status ...] [--authority ...]
    [--format jsonl|markdown]                       (默认 jsonl)
    --out path
```

**输出格式**：
- `list` 默认 ASCII 表格：`id | claim | authority | evidence_score | status | scope`；`--json` 切换 JSON Lines
- `show` 输出单条 claim 全文 + 元数据
- `why` 输出多段：原始来源、提取时间、作用域、确认历史（每条 evidence 显式列出）、冲突链（superseded/disputed 双方）、最近 5 次 recall（带 outcome）
- `export` 格式由 `--format` 决定；JSON Lines 每行一条 memory，markdown 是人读友好

**退出码**：

| 退出码 | 含义 |
|---|---|
| 0 | 成功 |
| 1 | 数据库错误 / 写入失败 |
| 2 | 参数错误（如未知 authority / scope 值） |
| 3 | 找不到指定 id |
| 4 | Authority 守门拒绝（如给 explicit 写但 source_type 不匹配） |
| 5 | 冲突无法解决（双方 Authority 相等且 scope 完全重叠） |

## 6. 三级召回 + Agent 集成（Section 4 已确认）

### 6.1 三级召回

1. **常驻能力说明**（每次 system prompt 一段固定文本，约 80 tokens）：
   > 你可使用 `mengdie memory` 子命令管理项目级记忆。本会话开始时已召回与当前项目相关的若干候选；如需更多上下文，用 `mengdie memory list --scope project` 浏览。

2. **任务级主题目录**（每个 turn 前一次性召回，注入到 system 消息尾部）：
   - 候选：项目 scope 下 `status=active` 的所有 memory
   - 排序：`evidence_score DESC, observed_at DESC`
   - 输出格式：`id | claim 前 60 字符`（每条约 80 字符）
   - 数量：默认取 20 条；总量 > 200 时改用「关键词预筛 + topK」

3. **原子记忆正文**（Agent 在 turn 中通过 `memory recall` 工具按需调）：
   - FTS5 全文检索 `memories_fts WHERE claim MATCH ?`
   - 过滤：`status='active' AND (valid_until IS NULL OR valid_until > now)`
   - 评分：
     ```text
     score = -bm25(claim MATCH ?)
           + authority_weight[authority]      // explicit=1.0, verified=0.8, repository=0.6, inferred=0.3
           + evidence_score                    // 0..N
           + task_scope_match * 0.5            // scope_kind='project' && project_identity 匹配 +0.5
           - conflict_penalty * 1.0            // status='disputed' -0.5, status='stale' -0.3
     ```
   - 默认 `topK=5`；`memory recall --topK 10 "关键词"` 覆盖
   - 召回同时调用 `Store.RecordUsage(memory_id, session_id, outcome=unknown)`

### 6.2 Agent 集成

`agent.Options` 新增字段：

```go
type Options struct {
    // ... 既有字段 ...
    MemoryRetriever MemoryRetriever
    ProjectIdentity string
}

type MemoryRetriever interface {
    Recall(ctx context.Context, query string, topK int) ([]RecallHit, error)
}
type RecallHit struct {
    ID           string
    Claim        string
    Authority    string
    EvidenceScore float64
    Score        float64
    SourceRef    string
}
```

`Agent.Run` 第一个 turn 前：如果 `MemoryRetriever != nil && state.Turn == 0`，调一次 `Recall(ctx, task, 20)`，把 hits 序列化为 markdown bullet list，注入到发给 Provider 的 system 消息尾部（**不修改**私有 context log；只影响请求体）。后续 turn 不再重复。

新增 `internal/tools/memory_recall.go` 工具（`effect=state`，无需 patch journal / approval），让 Agent 在 turn 中按需主动调 `memory recall "关键词" --topK 5`。Tool 输出是 markdown 列表（id + claim + source_ref）。

新增 `agent.Options.MemoryExtractor` 接口（占位 nil），M3 Slice 02 才实现：

```go
type MemoryExtractor interface {
    Extract(ctx context.Context, sessionID string) ([]Memory, error)
}
```

### 6.3 集成测试

`internal/app/agent_memory_test.go` 用一个独立 stub Provider（结构与 `internal/evaluation/chaos.scriptedProvider` 相似，但不依赖 chaos 包；可放在 `internal/app/agent_memory_test_helpers_test.go`）：

- 场景：Agent 启动时 MemoryRetriever 返回 3 条 project memory，stub Provider 看到 system 消息尾部包含这 3 条；运行 `memory recall` 工具时 stub 返回 `memory list` 输出
- 断言：system 消息包含 memory 的 claim 文本；MemoryRetriever 被调一次；工具被调一次

## 7. Memory Trust Set（Section 5 已确认）

`evals/memory/trust-set-v1.json`：30 个场景，每条结构：

```json
{
  "schema_version": 1,
  "id": "user-confirms-test-command",
  "category": "explicit",
  "description": "用户在对话里说「用 go test ./... 作为测试入口」",
  "setup": {
    "seed_memories": [
      {"claim": "项目测试入口是 go test ./internal/foo", "authority": "inferred", "status": "proposed"}
    ]
  },
  "actions": [
    {"type": "remember_user", "claim": "用 go test ./... 作为测试入口"}
  ],
  "expected": {
    "memory_present": true,
    "claim_match": "用 go test ./... 作为测试入口",
    "authority": "explicit",
    "status": "active",
    "evidence_score_gte": 0.5,
    "recallable": true,
    "forbid_duplicate": true,
    "forbid_old_status_change": false
  }
}
```

**30 个场景类别细分**（按用户选定：explicit 15 / repository 5 / verified 5 / inferred 5）：

| 类别 | 数量 | 覆盖点 |
|---|---|---|
| `explicit_user` | 15 | 重复纠正 / 新增明确规则 / supersede 旧 / 跨项目 scope / 显式 forget / `--valid-until` / `--hard` 删除 / export markdown 模式 / list `--json` / list scope 过滤 / list authority 过滤 / show 不存在 id 退出码 3 / 守门拒绝时退出码 4 / 冲突不覆盖 explicit / 跨 Branch scope |
| `repository` | 5 | 仓库配置文件 / build 文件 / CI workflow / AGENTS.md / git 提交事实 |
| `verified` | 5 | go test 通过 / 命令退出码 / lint 通过 / govulncheck 干净 / go build 跨平台 |
| `inferred` | 5 | 模型从成功 run 抽取的事实，必须经 `memory approve` 才可 active |

**评测指标**（`go test ./internal/memory/trustset -run TestMemoryTrustSetV1` 输出到 `evidence/memory-trust-v1.json`）：

| 指标 | 计算 | 目标 |
|---|---|---|
| precision@5 | ground-truth 出现在 top5 召回的比例 | ≥ 0.85 |
| false_recall_rate | 错误 status（archived / disputed）或越权 Authority 出现在 top5 的比例 | ≤ 0.05 |
| source_traceability | 召回条目中带 ≥ 1 条 memory_evidence 的比例 | ≥ 0.90 |
| authority_fidelity | Authority 守门通过率（不允许 inferred 直接 active） | = 1.0 |
| `why` completeness | `mengdie memory why` 输出包含 source / 提取时间 / 作用域 / 确认历史 / 冲突链 / 最近召回 全部 6 段的比例 | ≥ 0.90 |

## 8. 质量门禁与交付清单（Section 6 已确认）

### 8.1 质量门禁

- `go fmt ./...`
- `go vet ./...`
- `go test -race ./...`（除已知 Windows 控制台编码的 `TestShellExecute...`）
- `golangci-lint run ./...`（0 issue）
- `govulncheck@v1.1.4 ./...`（无漏洞）
- `CGO_ENABLED=0 go build ./cmd/...` 四目标（darwin-arm64、darwin-amd64、windows-amd64、linux-amd64）
- `go test ./internal/memory/... -run TestMemoryTrustSetV1` 通过；30 场景 100% pass
- `liveprovider` build tag 下：`go test -tags=liveprovider -run TestLiveProviderMemoryEndToEnd ./internal/memory/...` 通过真实 Provider（DeepSeek 或 Kimi，依赖环境变量）跑通端到端
- evidence JSON 不含 API Key、任务正文、用户代码片段

### 8.2 交付清单（按 7 个 phase 顺序）

| Phase | 内容 | 验证 |
|---|---|---|
| **0** | internal/memory: SQLite schema + FTS5 + 同步触发器 + rebuild；008_memory.sql 迁移 | `go test ./internal/session -run TestMigrations` 通过 |
| **1** | internal/memory/store: Save / List / Get / Why / Forget / RecordEvidence / RecordUsage；Authority 守门 + Conflict 状态机 | `go test ./internal/memory -run TestStore` 通过 |
| **2** | internal/memory/retrieve: 三级召回实现 + 评分公式 + record_usage | `go test ./internal/memory -run TestRetrieve` 通过 |
| **3** | internal/tools/memory_recall.go + internal/app/memory.go（9 个子命令） | `go test ./cmd/mengdie -run TestMemory` 通过 |
| **4** | agent.Options.MemoryRetriever + ProjectIdentity + 第一个 turn 前召回 + stub Provider 集成测试 | `go test ./internal/app -run TestAgentMemory` 通过 |
| **5** | evals/memory/trust-set-v1.json (30 场景) + runner + 指标输出到 `evidence/memory-trust-v1.json` | `go test ./internal/memory/trustset` 通过 |
| **6** | liveprovider 端到端 + CI 集成（`.github/workflows/ci.yml` 加 memory 步骤；新增 `memory-live-provider.yml` schedule） + 文档（`docs/development/phase-3-slice-01/`） + `bd close mengdie-n47` | CI 全过；新 PR ready-for-review |

## 9. 风险与回滚

| 风险 | 缓解 |
|---|---|
| FTS5 同步触发器在大量写入下性能差 | v0.1 不写热路径；评测时全在 SQLite 内存盘 / 临时文件；10 万条内无明显退化 |
| evidence_score 与 confidence 概念混淆 | Go struct 上两个独立字段；评测指标里只考核 `evidence_score` |
| 召回评分公式权重拍脑袋 | M3 Slice 01 跑出 baseline；M3 Slice 02 用真实 recall outcome 反馈调权 |
| Agent 集成导致 system prompt 膨胀 | 任务级主题目录仅在 turn 0 注入；不增量累积 |
| 跨平台 SQLite FTS5 兼容 | 已有 P1-12 DeepSeek 双平台 10/10 证据；modernc.org/sqlite 跨平台稳定 |

## 10. 不在范围内的明确条目

- 任务结束自动候选提取（`MemoryExtractor` 接口保留空实现）
- embedding / 向量检索（v0.1 仅 FTS5；ARCHITECTURE §8.7 暂缓）
- Reflect / Consolidate / 复盘报告（M4 切片）
- 跨项目记忆复制 / 同步
- 任何对 session 现有 schema 的改动
- daemon / 多客户端共享记忆（M5 范畴）
