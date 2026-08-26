# M4 Slice 01 — Reflect / Consolidate v0.1 手动复盘提案 设计

> 状态：草稿，待用户按 §分段确认后转入 writing-plans。
> 日期：2026-08-26
> 关联 Beads：`mengdie-z48` (claimed)
> 关联架构：`ARCHITECTURE.md §9（复盘机制）`

---

## 1. 背景与目标

### 1.1 背景

ARCHITECTURE §9 规划了 Reflect / Consolidate 复盘机制：5 阶段流水线（Scan → Extract → Verify → Reflect → Propose）让 Agent 周期性回顾 session，识别跨任务重复模式，并生成"可审核提案"而非自动改动。v0.1 阶段明文规定：

> 首版仅支持手动执行与会话结束提示，不默认启用定时无人值守任务（arch §9.2）。

M3 Slice 01–04 落地了 M3 的可信记忆闭环（schema / extractor / auto-Approve / dispute 标记）。M4 的复盘机制建立在 M3 之上：**Reflect 的输入是已落地的 session 事件与 memory 候选；输出是 proposal 表里的可审核条目。**

### 1.2 目标

落地最小可用的 v0.1 复盘提案闭环：

1. **`proposals` 表 + ProposalStore**：新增 `internal/memory/proposal/` 子包，存储 reflect 生成的提案
2. **CLI `mengdie reflect`**：4 个子命令（`reflect` / `reflect proposals` / `reflect approve` / `reflect reject`）
3. **5 阶段流水线 v0.1 简化版**：
   - **Scan**：从 session_store 拉最近 N 个 session 的 events
   - **Extract**：复用 M3 Slice 02 的 Rules + LLM extractor（不重写）
   - **Verify**：复用 extractor 已有的 `source_type` 与 `confidence` 字段；无需新增
   - **Reflect**：rule-based 模式检测（v0.1 简化）；LLM-based 留 v0.2
   - **Propose**：写 proposals 表（status=proposed）+ 输出 CLI 报告
4. **安全边界**：
   - Reflect Worker 工具集只有读（events / sessions / memory / files）
   - 写只到 `proposals` 表；不直接改 AGENTS.md、Skill、Policy
   - 用户显式 `reflect approve <id>` 后由 driver 走普通 Policy + Approval 链路（v0.1 不实现 driver，approve 仅把 proposal status=approved + 写一个 approved_marker memory）
5. **Trust Set 增量场景**：5 个新场景覆盖 reflect 流水线各阶段

### 1.3 不在本切片范围（明确写出避免范围漂移）

- 自动 resolve / 自动 merge（v0.2+）
- Daemon / idle / cron 触发（arch §9.2 显式延期到 daemon 落地后）
- LLM-based Reflect（v0.1 用 rule-based；v0.2 加 LLM mode）
- 自动 apply approve 到 AGENTS.md / Skill（v0.1 approve 仅标 status；v0.2 加 apply 子命令）
- Proposal 分级 / 评分模型（v0.1 简单 boolean accept/reject）
- 跨 scope consolidation（spec §4.2 row 4 仍 defer）

---

## 2. 数据模型

### 2.1 新增 migration `010_reflection_proposals.sql`

```sql
CREATE TABLE reflection_proposals (
    rowid        INTEGER PRIMARY KEY,
    id           TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL,        -- memory_upgrade | agents_md_revision | skill_draft | obsolete
    title        TEXT NOT NULL,
    body         TEXT NOT NULL,        -- JSON-encoded ProposalBody
    status       TEXT NOT NULL,        -- proposed | approved | rejected
    based_on     TEXT NOT NULL,        -- JSON list of source memory/session ids
    session_id   TEXT,
    confidence   REAL NOT NULL,       -- 0.0-1.0
    evidence     TEXT NOT NULL,       -- JSON-encoded EvidenceList
    observed_at  TEXT NOT NULL,       -- RFC3339Nano UTC
    reviewed_at  TEXT,
    reviewer     TEXT,                -- empty until status != proposed
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE INDEX idx_proposals_status_observed
    ON reflection_proposals (status, observed_at DESC);

CREATE INDEX idx_proposals_session
    ON reflection_proposals (session_id);
```

### 2.2 `Proposal` 类型（`internal/memory/proposal/`）

```go
type ProposalKind string  // "memory_upgrade" | "agents_md_revision" | "skill_draft" | "obsolete"

type ProposalStatus string  // "proposed" | "approved" | "rejected"

type Proposal struct {
    ID         string
    Kind       ProposalKind
    Title      string
    Body       ProposalBody  // JSON-encoded
    Status     ProposalStatus
    BasedOn    []string  // source memory / session ids
    SessionID  string
    Confidence float64
    Evidence   []Evidence
    ObservedAt time.Time
    ReviewedAt *time.Time
    Reviewer   string
    CreatedAt  time.Time
    UpdatedAt  time.Time
}

type ProposalBody struct {
    // memory_upgrade: { "memory_id": "mem_xxx", "current_claim": "...", "proposed_claim": "..." }
    // agents_md_revision: { "section": "## X", "current": "...", "proposed": "..." }
    // skill_draft: { "skill_name": "...", "description": "...", "frontmatter": {...} }
    // obsolete: { "memory_id": "mem_xxx", "reason": "..." }
    Kind    string         `json:"kind"`
    Payload map[string]any `json:"payload"`
}
```

### 2.3 `ProposalStore`（`internal/memory/proposal/store.go`）

```go
type Store struct {
    db  *sql.DB
    now func() time.Time
}

func Open(db *sql.DB, now func() time.Time) *Store  // 或从 OpenMemory 获取

func (s *Store) List(ctx context.Context, q ListQuery) ([]Proposal, error)
func (s *Store) Get(ctx context.Context, id string) (Proposal, error)
func (s *Store) Insert(ctx context.Context, p Proposal) (Proposal, error)
func (s *Store) UpdateStatus(ctx context.Context, id string, status ProposalStatus, reviewer string) error

type ListQuery struct {
    Status   ProposalStatus
    Kind     ProposalKind
    SessionID string
    Since    time.Time
    Limit    int
}
```

### 2.4 error sentinels

```go
var (
    ErrProposalNotFound = errors.New("proposal not found")
    ErrInvalidProposal  = errors.New("invalid proposal")
)
```

---

## 3. 5 阶段流水线（v0.1 简化）

### 3.1 Pipeline 接口（`internal/memory/proposal/pipeline.go`）

```go
type Pipeline struct {
    sessionStore *session.SQLiteStore
    memoryStore  *memory.Store
    proposalStore *Store
    now          func() time.Time
}

func New(...) *Pipeline

// Reflect runs the full 5-stage pipeline and writes proposals.
// Returns the proposals created during this run.
func (p *Pipeline) Reflect(ctx context.Context, opts ReflectOptions) ([]Proposal, error)

type ReflectOptions struct {
    Since       time.Time  // if zero, default = 7 days ago
    SessionIDs  []string   // if non-empty, scan only these; else "last N sessions"
    MaxSessions int        // default 5
}
```

### 3.2 Stage 1: Scan

读取最近 `MaxSessions` 个 session 的 events（v0.1 限制为 SessionStore.Events(sessionID, 1000)）。每个 session 产出：

```go
type ScannedSession struct {
    SessionID string
    Events    []events.Envelope  // 全部事件，按
    FirstRunAt time.Time
    LastRunAt  time.Time
}
```

Scan 阶段不引入新依赖，复用既有 `session.SQLiteStore.Events`。

### 3.3 Stage 2: Extract

复用 M3 Slice 02 的 `extractor.NewRules(reader)` 与 `extractor.NewLLM(stub, model, reader)`。v0.1 不用 Hybrid——Hybrid 是 M3 slice 02 加的优化路径，这里 rules-only + 可选 LLM 都可。直接复用 hybrid。

extractor 输出 candidate memories；pipeline 标 `based_on = [session_id]`，`confidence = 候选 confidence`，evidence 用 extractor 的 evidence_score。

### 3.4 Stage 3: Verify

v0.1 简化：复用 extractor 已有的 `source_type`（`file` / `command_result` / `agent_message` / `user_message`）；无需新增 verify 逻辑。LLM 候选保留 `confidence` 字段（spec §4.1）。

### 3.5 Stage 4: Reflect（v0.1 rule-based 简化）

定义 5 条 pattern（v0.1 内置；v0.2 可由 LLM 推断）：

| Pattern | Trigger | ProposalKind |
|---|---|---|
| `repeated-correction` | 同一 session 内 ≥3 个 user_message 包含 "no," "don't," "wrong" 类词 | `agents_md_revision` (用户拒绝某些做法 → 修订 AGENTS.md) |
| `repeated-tool-preference` | 同一 session ≥80% tool 调是 edit_file，剩 20% 是 write_file | `memory_upgrade` (升级已有 edit_file fingerprint claim) |
| `forgotten-test` | shell 命令 "go test ./..." 失败 ≥2 次 | `memory_upgrade` (更新已有 testing-claim) |
| `cross-session-pattern` | 同一 scope + 同一 authority 的相同 claim 在 ≥3 个 session 重复出现 | `memory_upgrade` (升级为显式 memory) |
| `obsolete-claim` | valid_until 到期 + status=stale | `obsolete` |

每条 pattern 命中时产出 1 个 proposal：

```go
proposal := Proposal{
    Kind: ProposalKindMemoryUpgrade,
    Title: "升级记忆：项目测试入口是 go test ./...",
    Body: ProposalBody{Payload: map[string]any{
        "memory_id":      "mem_xxx",
        "current_claim":  "...",
        "proposed_claim": "...",
        "reason":         "在 3 个 session 重复出现，建议升级 authority",
    }},
    BasedOn:   []string{"mem_xxx", "session_a", "session_b", "session_c"},
    SessionID: "session_a",
    Confidence: 0.7,
    Status:    StatusProposed,
}
```

### 3.6 Stage 5: Propose

`Pipeline.Reflect` 调 `proposalStore.Insert` 把每条 proposal 落到 `reflection_proposals` 表。CLI 报告输出到 stdout：

```text
Generated 3 proposals (since 7d, 5 sessions scanned):
  prop_xxx  memory_upgrade  "升级记忆：项目测试入口..." (confidence 0.7)
  prop_yyy  agents_md_revision  "修订 AGENTS.md：..." (confidence 0.5)
  prop_zzz  obsolete  "..." (confidence 0.9)
```

---

## 4. CLI 设计

### 4.1 dispatcher

`internal/app/cli.go`（或 `reflect.go`）的 dispatcher 加 `case "reflect":`，5 个子动词：

```text
mengdie reflect                  # 跑 Reflect 流水线（默认 since 7d）
mengdie reflect --since 7d       # 时间窗口
mengdie reflect --max-sessions 5 # 上限
mengdie reflect proposals        # 列出 proposals
mengdie reflect proposals --status proposed
mengdie reflect approve <id>    # 标 status=approved + reviewer=<user>
mengdie reflect reject <id>     # 标 status=rejected + reviewer=<user>
```

### 4.2 `runReflect`（pipeline 触发）

```go
func runReflect(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
    flags, common := a.newMemoryFlagSet("mengdie reflect", stderr)
    since := flags.String("since", "7d", "时间窗口 (e.g. 7d, 24h)")
    maxSessions := flags.Int("max-sessions", 5, "最大扫描 session 数")
    if err := flags.Parse(args); err != nil {
        return flagExitCode(err)
    }
    // 解析 since → time.Time (默认 7d)
    sinceTime, err := parseSince(*since, a.now())
    if err != nil {
        // 写 stderr, exit 2
    }
    pipeline, _, _, code := a.openReflectPipeline(ctx, common)
    if code != ExitOK { return code }
    defer ...
    proposals, err := pipeline.Reflect(ctx, ReflectOptions{
        Since: sinceTime,
        MaxSessions: *maxSessions,
    })
    if err != nil { return exitForStoreError(err) }
    // 输出 Generated N proposals ... 报告
}
```

### 4.3 `runReflectProposals`（list）

复用 `writeMemoryListTable` 风格；输出 `id | kind | title | confidence | status | observed_at`。

### 4.4 `runReflectApprove` / `runReflectReject`

```go
func runReflectApprove(ctx context.Context, args []string, a *App, ...) int {
    if len(args) != 1 { ... exit 2 ... }
    id := args[0]
    proposalStore, _, _, code := a.openProposalStore(ctx, common)
    if code != ExitOK { return code }
    defer ...
    if err := proposalStore.UpdateStatus(ctx, id, StatusApproved, a.reviewer()); err != nil {
        return exitForStoreError(err)
    }
    fmt.Fprintf(stdout, "approved %s\n", id)
}
```

`reject` 同理。`a.reviewer()` 取 user / git config；v0.1 简化为 `os.Getenv("USER")` + `"mengdie"` fallback。

### 4.5 exit 码映射

扩展 `exitForStoreError`：

```go
case errors.Is(err, memoryproposal.ErrProposalNotFound):
    return ExitNotFound  // 3
```

`ExitOK / ExitInvalidInput / ExitRunError / ExitNotFound` 复用现有。

---

## 5. Trust Set 增量场景

Append to `evals/memory/trust-set-v1.json`：

| ID | category | description | expected |
|---|---|---|---|
| `reflect-scan-since-default` | reflect | 默认 since=7d 跑一次扫描，生成 ≥1 proposal | proposals 行 status=proposed |
| `reflect-scan-no-recent-sessions` | reflect | since=1h 且无近期 session → 0 proposals | proposals 行 0 行 |
| `reflect-proposal-memory-upgrade` | reflect | seed 3 个 session 同 claim → 触发 `cross-session-pattern` → 生成 memory_upgrade proposal | proposals 行 1 行 kind=memory_upgrade |
| `reflect-proposal-obsolete` | reflect | seed 1 个 stale memory → 触发 `obsolete-claim` | proposals 行 1 行 kind=obsolete |
| `reflect-approve-promotes-status` | reflect | approve proposal 后 status=approved, reviewer=USER, reviewed_at 非空 | approved row |

5 个新场景。distribution：现有 45 → 50。

Trust Set runner 扩展 `actions[].type` 加 `"reflect"` 与 `"reflect_propose"`：

- `reflect`: 跑 Pipeline.Reflect；生成 proposals
- `reflect_propose`: 在已有 session + memory fixture 上手动 insert 一条 proposal（用于 negative-control 与 stale 测试）

---

## 6. 不在本切片范围

- 自动 Apply（v0.2+）：`reflect approve` 后由 driver 写 AGENTS.md / Skill / memory upgrade
- LLM-based Reflect（v0.2+）：reflection pattern detection 由 LLM 推断
- Daemon / idle / cron（arch §9.2 显式延期）
- 跨 scope consolidation
- Proposal 评分模型（v0.1 简单 boolean）
- Proposal 草稿回滚

---

## 7. 风险与回滚

| 风险 | 缓解 |
|---|---|
| `proposals` migration 加表失败 → SQLite 升级不兼容 | migration 是纯 `CREATE TABLE` + 2 indexes；schema 增量无 break |
| Pipeline 误把过期证据当 fresh → 错误 upgrade | `ObservedAt` 字段记录生成时间；用户 review 时可见；status=proposed 默认不 apply |
| `reflect approve` 错配 reviewer → 信任链断 | `Reviewer` 字段强制；v0.1 fallback `"mengdie"`，v0.2 接 git config user |
| Pipeline 性能（N session × extract + reflect） | 默认 `--max-sessions=5`，`--since=7d` 限制 |
| Reflect Worker 越权（读+写 AGENTS.md） | v0.1 写只到 `proposals` 表；CLI 不暴露 filesystem write；arch §9.4 |

---

## 8. 验收标准（AC）

1. **proposals 表 migration** 通过；既有 008/009/010 链 load 测试不变
2. **Pipeline 5 阶段**端到端跑通：seed 5 session → Reflect → ≥3 proposals
3. **5 pattern 命中**：每个 pattern 单独 unit test 通过
4. **CLI 4 子命令**：`reflect` / `reflect proposals` / `reflect approve` / `reflect reject` 端到端
5. **Trust Set 50 场景**：45 旧 + 5 新 全过；auth_fid + why_complete + source_trace + false_recall 5 指标不退化
6. **5-metric baseline**：新增 `proposal_acceptance_rate` 与 `time_to_benefit`（v0.1 stub，可填 0.0）
7. **本地质量门禁**：`gofmt -l .` 0 / `go vet ./...` clean / 4 目标 build OK / golangci-lint 0 / govulncheck no vulns
8. **docs**：`docs/development/phase-4-slice-01/IMPLEMENTATION_REPORT.md` 创建 + `README.md` M4 勾选

---

## 9. 文件清单（task 拆分前的预估）

### 新增

- `internal/session/migrations/010_reflection_proposals.sql`
- `internal/memory/proposal/proposal.go` (`Proposal` / `ProposalBody` / `ListQuery`)
- `internal/memory/proposal/store.go` (`Store` + SQL CRUD)
- `internal/memory/proposal/pipeline.go` (`Pipeline` + 5 阶段)
- `internal/memory/proposal/patterns.go` (5 内置 pattern)
- `internal/memory/proposal/proposal_test.go`
- `internal/memory/proposal/pipeline_test.go`
- `internal/memory/proposal/patterns_test.go`
- `internal/app/reflect.go` (`runReflect*` 4 函数 + dispatcher)
- `internal/app/reflect_test.go`
- `docs/development/phase-4-slice-01/IMPLEMENTATION_REPORT.md`

### 修改

- `internal/app/cli.go`（dispatcher 加 case "reflect"）
- `evals/memory/trust-set-v1.json`（5 新场景 + 新 action verbs）
- `internal/memory/trustset/runner.go`（新 action verbs: `reflect`, `reflect_propose`）
- `internal/memory/trustset/runner_test.go`（45 → 50 计数）
- `README.md`（M4 勾选）

### 不改

- `internal/memory/store.go`（proposals 是独立表，不动 memories）
- 008_memory.sql / 009_memory_source_command.sql
- go.mod

---

## 10. 分段确认

请按 § 分段 review，每段 ack 或给修改建议：

- [ ] §1 背景与目标
- [ ] §2 数据模型（proposals 表 + Proposal 类型）
- [ ] §3 5 阶段流水线（v0.1 简化：rule-based Reflect）
- [ ] §4 CLI 设计（4 子命令 + exit 码）
- [ ] §5 Trust Set 5 新场景
- [ ] §6 不在范围
- [ ] §7 风险
- [ ] §8 验收标准
- [ ] §9 文件清单

收到全部 ack 后转入 writing-plans 写实施计划。