# M4 Slice 01 Implementation Plan — Reflect / Consolidate v0.1 手动复盘提案

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 ARCHITECTURE §9 的 Reflect / Consolidate v0.1：proposals 表 + 5 阶段流水线 + `mengdie reflect` CLI 4 子命令。Trust Set 45 → 50 场景。默认只生成 proposal，apply 留 v0.2。

**Architecture:** 新 migration `010_reflection_proposals.sql` 加 proposals 表 + 2 索引；新 `internal/memory/proposal/` 子包含 Store / Pipeline / patterns；CLI 4 子命令接入现有 `internal/app/cli.go` dispatcher。Pipeline 复用 M3 Slice 02 hybrid extractor（不重写 extract），Reflect 阶段用 5 条 rule-based pattern（v0.1 简化）。

**Tech Stack:** Go 1.26.6, `modernc.org/sqlite`, `internal/session`（既有）, `internal/memory/extractor`（复用）.

## Global Constraints

- Go 1.26.6，module `github.com/Scorpio69t/mengdie-code`
- 仅 `CGO_ENABLED=0` 跨平台；四目标必须通过：darwin-arm64、darwin-amd64、windows-amd64、linux-amd64
- 禁止在用户仓库中自动 git commit / push
- 任何 `git commit` 由执行人显式触发
- 不修改既有 `008_memory.sql` / `009_memory_source_command.sql`；本切片新增 `010_reflection_proposals.sql`（纯 CREATE TABLE）
- 不修改 `internal/memory/store.go`（proposals 是独立表，不入 memories 表）
- 复用 `internal/memory/extractor`（hybrid + rules + llm）；不重写 extract
- proposals 表只在 reflect 流水线写；CLI 不暴露 filesystem write
- v0.1 `reflect approve` 仅标 status=approved + reviewer；不 apply（apply 留 v0.2）
- 错误统一用 `errors.New(...)` + `fmt.Errorf("%w", sentinel)`
- 中文优先 package doc + 英文 inline comments
- 不引入新第三方依赖

---

## File Structure

### 新增

- `internal/session/migrations/010_reflection_proposals.sql`
- `internal/memory/proposal/proposal.go` (`Proposal` / `ProposalBody` / `ListQuery` / sentinel)
- `internal/memory/proposal/store.go` (`Store` + SQL CRUD)
- `internal/memory/proposal/pipeline.go` (`Pipeline` + 5 阶段骨架)
- `internal/memory/proposal/patterns.go` (5 内置 pattern)
- `internal/memory/proposal/proposal_test.go`
- `internal/memory/proposal/pipeline_test.go`
- `internal/memory/proposal/patterns_test.go`
- `internal/app/reflect.go` (`runReflect*` 4 函数 + helpers)
- `internal/app/reflect_test.go`
- `docs/development/phase-4-slice-01/IMPLEMENTATION_REPORT.md`

### 修改

- `internal/app/cli.go`（dispatcher 加 case "reflect"）
- `evals/memory/trust-set-v1.json`（5 新场景 + 新 action verbs）
- `internal/memory/trustset/runner.go`（新 action verbs: `reflect`, `reflect_propose`）
- `internal/memory/trustset/runner_test.go`（45 → 50 计数）
- `internal/memory/store.go`（exitForStoreError 路径扩展在 `internal/app` 而非 store，所以这里不动）
- `internal/app/memory.go`（`exitForStoreError` 加 `ErrProposalNotFound → ExitNotFound`）
- `README.md`（M4 勾选）

### 不改

- `internal/memory/store.go`（proposals 不入 memories 表）
- 008_memory.sql / 009_memory_source_command.sql
- go.mod / go.sum

---

### Interfaces Introduced

```go
// internal/memory/proposal/proposal.go (new)
type ProposalKind string  // "memory_upgrade" | "agents_md_revision" | "skill_draft" | "obsolete"
type ProposalStatus string  // "proposed" | "approved" | "rejected"
type Proposal struct { ... }
type ProposalBody struct { ... }
type ListQuery struct { ... }
var (
    ErrProposalNotFound = errors.New("proposal not found")
    ErrInvalidProposal  = errors.New("invalid proposal")
)

// internal/memory/proposal/store.go (new)
type Store struct { db *sql.DB; now func() time.Time }
func Open(db *sql.DB, now func() time.Time) *Store
func (s *Store) List(ctx, q) ([]Proposal, error)
func (s *Store) Get(ctx, id) (Proposal, error)
func (s *Store) Insert(ctx, p) (Proposal, error)
func (s *Store) UpdateStatus(ctx, id, status, reviewer) error

// internal/memory/proposal/pipeline.go (new)
type Pipeline struct { ... }
func New(...) *Pipeline
func (p *Pipeline) Reflect(ctx, opts ReflectOptions) ([]Proposal, error)
type ReflectOptions struct { Since time.Time; SessionIDs []string; MaxSessions int }

// internal/memory/proposal/patterns.go (new)
func DetectRepeatedCorrection(events []events.Envelope) []Proposal
func DetectRepeatedToolPreference(events []events.Envelope) []Proposal
func DetectForgottenTest(events []events.Envelope) []Proposal
func DetectCrossSessionPattern(sessions []ScannedSession, memStore *memory.Store) []Proposal
func DetectObsoleteClaim(memStore *memory.Store) []Proposal
```

---

### Task 1: proposals 表 migration + ProposalStore 基础

**Files:**
- Create: `internal/session/migrations/010_reflection_proposals.sql`
- Create: `internal/memory/proposal/proposal.go`
- Create: `internal/memory/proposal/store.go`
- Create: `internal/memory/proposal/proposal_test.go`

**Interfaces:**
- Consumes: `internal/session.SQLiteStore.DB()` (既有)
- Produces: `Store` with List / Get / Insert / UpdateStatus

- [ ] **Step 1: 写失败测试**

In `internal/memory/proposal/proposal_test.go`（新建）：

``` `op```go
package proposal_test

import (
    "context"
    "path/filepath"
    "testing"
    "time"

    "github.com/Scorpio69t/mengdie-code/internal/memory/proposal"
    "github.com/Scorpio69t/mengdie-code/internal/session"
)

var proposalTestTime = time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)

func openProposalStore(t *testing.T) (*proposal.Store, *session.SQLiteStore) {
    t.Helper()
    dir := t.TempDir()
    sessionStore, err := session.OpenSQLite(context.Background(), session.OpenOptions{
        DataDir: dir, ProjectRoot: filepath.Join(t.TempDir(), "project"),
        Now: func() time.Time { return proposalTestTime },
    })
    if err != nil { t.Fatalf("OpenSQLite: %v", err) }
    return proposal.Open(sessionStore.DB(), func() time.Time { return proposalTestTime }), sessionStore
}

func TestProposalStoreInsertAndGet(t *testing.T) {
    ctx := context.Background()
    store, sessionStore := openProposalStore(t)
    defer func() { _ = sessionStore.Close() }()

    p := proposal.Proposal{
        Kind: proposal.ProposalKindMemoryUpgrade,
        Title: "升级记忆：项目测试入口是 go test ./...",
        Body: proposal.ProposalBody{
            Kind: "memory_upgrade",
            Payload: map[string]any{
                "memory_id": "mem_xxx",
                "current_claim": "...",
                "proposed_claim": "...",
            },
        },
        BasedOn: []string{"mem_xxx", "session_a"},
        SessionID: "session_a",
        Confidence: 0.7,
        Status: proposal.StatusProposed,
        ObservedAt: proposalTestTime,
    }
    saved, err := store.Insert(ctx, p)
    if err != nil { t.Fatalf("Insert: %v", err) }
    if saved.ID == "" { t.Fatal("ID empty") }

    got, err := store.Get(ctx, saved.ID)
    if err != nil { t.Fatalf("Get: %v", err) }
    if got.Title != p.Title { t.Fatalf("Title mismatch") }
    if got.Status != proposal.StatusProposed { t.Fatalf("Status want proposed, got %s", got.Status) }
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory/proposal -count=1 -v`
Expected: FAIL（package 不存在）

- [ ] **Step 3: 写 migration `010_reflection_proposals.sql`**

Create `internal/session/migrations/010_reflection_proposals.sql`：

```sql
-- 010_reflection_proposals.sql
-- M4 Slice 01：复盘提案表。Reflect Worker 写的可审核条目；不直接改 AGENTS.md / Skill / memory。
-- 来源：mengdie reflect CLI 触发的 5 阶段流水线（Scan → Extract → Verify → Reflect → Propose）。
CREATE TABLE reflection_proposals (
    rowid        INTEGER PRIMARY KEY,
    id           TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL,        -- memory_upgrade | agents_md_revision | skill_draft | obsolete
    title        TEXT NOT NULL,
    body         TEXT NOT NULL,        -- JSON-encoded ProposalBody
    status       TEXT NOT NULL,        -- proposed | approved | rejected
    based_on     TEXT NOT NULL,        -- JSON-encoded list of source ids
    session_id   TEXT,
    confidence   REAL NOT NULL,
    evidence     TEXT NOT NULL,        -- JSON-encoded EvidenceList
    observed_at  TEXT NOT NULL,
    reviewed_at  TEXT,
    reviewer     TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE INDEX idx_proposals_status_observed ON reflection_proposals (status, observed_at DESC);
CREATE INDEX idx_proposals_session ON reflection_proposals (session_id);
```

- [ ] **Step 4: 实现 `Proposal` / `ProposalBody` / `ListQuery` / sentinels**

In `internal/memory/proposal/proposal.go`：

```go
package proposal

import (
    "errors"
    "time"
)

type ProposalKind string
type ProposalStatus string

const (
    KindMemoryUpgrade     ProposalKind = "memory_upgrade"
    KindAgentsMdRevision  ProposalKind = "agents_md_revision"
    KindSkillDraft        ProposalKind = "skill_draft"
    KindObsolete          ProposalKind = "obsolete"

    StatusProposed ProposalStatus = "proposed"
    StatusApproved ProposalStatus = "approved"
    StatusRejected ProposalStatus = "rejected"
)

var (
    ErrProposalNotFound = errors.New("proposal not found")
    ErrInvalidProposal  = errors.New("invalid proposal")
)

type Proposal struct {
    ID         string
    Kind       ProposalKind
    Title      string
    Body       ProposalBody
    Status     ProposalStatus
    BasedOn    []string
    SessionID  string
    Confidence float64
    Evidence   []Evidence  // re-use memory.Evidence or duplicate? Read brief: keep simple
    ObservedAt time.Time
    ReviewedAt *time.Time
    Reviewer   string
    CreatedAt  time.Time
    UpdatedAt  time.Time
}

type ProposalBody struct {
    Kind    string         `json:"kind"`
    Payload map[string]any `json:"payload"`
}

type Evidence struct {
    Kind        string  `json:"kind"`
    Description string  `json:"description"`
    Confidence  float64 `json:"confidence"`
}

type ListQuery struct {
    Status    ProposalStatus
    Kind      ProposalKind
    SessionID string
    Since     time.Time
    Limit     int
}
```

> **注**：先确认 `memory.Evidence` 是否能 reuse，否则在 proposal package 里新定义一个 minimal `Evidence` type（`kind`/`description`/`confidence`）。读 `internal/memory/memory.go` 确认。

- [ ] **Step 5: 实现 `Store` (CRUD)**

In `internal/memory/proposal/store.go`：

```go
package proposal

import (
    "context"
    "crypto/sha256"
    "database/sql"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "strings"
    "time"
)

const (
    insertProposalSQL = =`INSERT INTO reflection_proposals
        (id, kind, title, body, status, based_on, session_id, confidence,
         evidence, observed_at, reviewed_at, reviewer, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
)

type Store struct {
    db  *sql.DB
    now func() time.Time
}

func Open(db *sql.DB, now func() time.Time) *Store {
    return &Store{db: db, now: now}
}

func (s *Store) List(ctx context.Context, q ListQuery) ([]Proposal, error) { ... }
func (s *Store) Get(ctx context.Context, id string) (Proposal, error) { ... }
func (s *Store) Insert(ctx context.Context, p Proposal) (Proposal, error) { ... }
func (s *Store) UpdateStatus(ctx context.Context, id string, status ProposalStatus, reviewer string) error { ... }

// generateProposalID mirrors memory.GenerateID (read existing impl)
func generateProposalID(now time.Time, kind ProposalKind) string { ... }
```

完整实现遵循既有 `internal/memory/store.go` 的 CRUD pattern（scanMemoryFields / tx.Commit / etc.）。`Body` 字段用 `encoding/json.Marshal` 序列化为 TEXT。

> **重要**：先 `Read internal/memory/store.go` 看 scanMemoryFields 怎么实现；proposal scanProposalFields 镜像写。GenerateID 看 `internal/memory/memory.go:GenerateID`。

- [ ] **Step 6: 跑测试确认 pass**

Run: `go test ./internal/memory/proposal -count=1 -v`
Expected: PASS

- [ ] **Step 7: 跑全 session migration 链确认 010 不破坏既有**

Run: `go test ./internal/session -count=1`
Expected: 全 PASS（010 migration load 测试 `TestArtifactMigrationUpgradesExistingContextLedger` 计数 8 → 9 + 1 = 10）

- [ ] **Step 8: Commit**

```bash
git add internal/session/migrations/010_reflection_proposals.sql internal/memory/proposal/
git commit -m "feat(memory): add reflection_proposals migration + ProposalStore"
```

---

### Task 2: Pipeline 5 阶段骨架 + Scan + Extract + Verify

**Files:**
- Create: `internal/memory/proposal/pipeline.go`
- Create: `internal/memory/proposal/pipeline_test.go`

**Interfaces:**
- Consumes: `SessionStore` (既有) + `MemoryStore` (既有) + `ProposalStore` (Task 1)
- Produces: `Pipeline.Reflect(ctx, ReflectOptions) ([]Proposal, error)`

- [ ] **Step 1: 写失败测试**

In `internal/memory/proposal/pipeline_test.go`：

```go
func TestPipelineReflectNoSessions(t *testing.T) {
    // fresh empty store, Reflect should return []Proposal, nil
}

func TestPipelineReflectScansEvents(t *testing.T) {
    // seed 1 session with tool.completed events → Reflect → ≥0 proposals
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory/proposal -run TestPipeline -count=1 -v`
Expected: FAIL（`Pipeline` undefined）

- [ ] **Step 3: 实现 `Pipeline` 骨架**

In `internal/memory/proposal/pipeline.go`：

```go
package proposal

import (
    "context"
    "fmt"
    "time"

    "github.com/Scorpio69t/mengdie-code/internal/events"
    "github.com/Scorpio69t/mengdie-code/internal/memory"
    "github.com/Scorpio69t/mengdie-code/internal/memory/extractor"
    "github.com/Scorpio69t/mengdie-code/internal/session"
)

type Pipeline struct {
    sessionStore   *session.SQLiteStore
    memoryStore    *memory.Store
    proposalStore  *Store
    now            func() time.Time
}

func New(ss *session.SQLiteStore, ms *memory.Store, ps *Store, now func() time.Time) *Pipeline {
    return &Pipeline{ss: ss, ms: ms, ps: ps, now: now}
}

type ScannedSession struct {
    SessionID  string
    Events     []events.Envelope
    FirstRunAt time.Time
    LastRunAt  time.Time
}

type ReflectOptions struct {
    Since       time.Time  // default: now - 7d
    SessionIDs  []string   // if empty, scan last MaxSessions
    MaxSessions int        // default 5
}

func (p *Pipeline) Reflect(ctx context.Context, opts ReflectOptions) ([]Proposal, error) {
    sessions, err := p.scan(ctx, opts)
    if err != nil { return nil, fmt.Errorf("scan: %w", err) }
    candidates, err := p.extract(ctx, sessions)
    if err != nil { return nil, fmt.Errorf("extract: %w", err) }
    verified := p.verify(candidates)
    proposals := p.reflect(ctx, verified, sessions)
    if err := p.propose(ctx, proposals); err != nil {
        return nil, fmt.Errorf("propose: %w", err)
    }
    return proposals, nil
}

// scan reads recent sessions' events (Stage 1)
func (p *Pipeline) scan(ctx context.Context, opts ReflectOptions) ([]ScannedSession, error) {
    // 读 sessionStore 列出最近 MaxSessions 个 session id
    // 每个 session 读 events (上限 1000 per session)
    // 返回 []ScannedSession
    ...
}

// extract runs hybrid extractor on each session (Stage 2)
func (p *Pipeline) extract(ctx context.Context, sessions []ScannedSession) ([]extractorMemory, error) {
    // 复用 internal/memory/extractor.NewHybrid / NewRules
    // 注意：v0.1 不依赖 LLM (避免 Provider env); 仅用 Rules + Hybrid fallback
    ...
}

// verify is pass-through (Stage 3)
func (p *Pipeline) verify(candidates []extractorMemory) []extractorMemory {
    return candidates  // v0.1 simplified
}

// reflect calls the 5 patterns (Stage 4)
func (p *Pipeline) reflect(ctx context.Context, candidates []extractorMemory, sessions []ScannedSession) []Proposal {
    proposals := []Proposal{}
    proposals = append(proposals, DetectRepeatedCorrection(sessions)...)
    proposals = append(proposals, DetectRepeatedToolPreference(sessions)...)
    proposals = append(proposals, DetectForgottenTest(sessions)...)
    proposals = append(proposals, DetectCrossSessionPattern(sessions, p.memoryStore)...)
    proposals = append(proposals, DetectObsoleteClaim(p.memoryStore)...)
    return proposals
}

// propose inserts each proposal into the store (Stage 5)
func (p *Pipeline) propose(ctx context.Context, proposals []Proposal) error {
    for _, p := range proposals {
        if _, err := p.proposalStore.Insert(ctx, p); err != nil {
            return err
        }
    }
    return nil
}
```

> **注**：`extractorMemory` 是 `[]memory.Memory` 的 alias；pipeline extract 阶段复用 M3 Slice 02 hybrid extractor。

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/memory/proposal -run TestPipeline -count=1 -v`
Expected: PASS

- [ ] **Step 5: 跑全 memory 测试确认无回归**

Run: `go test ./internal/memory -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/memory/proposal/pipeline.go internal/memory/proposal/pipeline_test.go
git commit -m "feat(memory): Reflect pipeline 5 stages (scan + extract + verify + reflect + propose)"
```

---

### Task 3: 5 Reflect patterns

**Files:**
- Create: `internal/memory/proposal/patterns.go`
- Create: `internal/memory/proposal/patterns_test.go`

**Interfaces:**
- Consumes: `events.Envelope` (既有) + `memory.Store` (既有)
- Produces: 5 pattern detection functions returning `[]Proposal`

- [ ] **Step 1: 写 5 个 pattern unit tests**

In `internal/memory/proposal/patterns_test.go`：

```go
func TestDetectRepeatedCorrection(t *testing.T) {
    // seed ≥3 user_message events with "no, " 之类词 → 1 proposal kind=agents_md_revision
}

func TestDetectRepeatedToolPreference(t *testing.T) {
    // seed 5 tool events: 4 edit_file + 1 write_file → 1 proposal kind=memory_upgrade
}

func TestDetectForgottenTest(t *testing.T) {
    // seed 2 shell events with summary="go test ... exit=1" → 1 proposal
}

func TestDetectCrossSessionPattern(t *testing.T) {
    // seed 3 sessions with same claim → 1 proposal
}

func TestDetectObsoleteClaim(t *testing.T) {
    // seed 1 memory with status=stale, valid_until past → 1 proposal kind=obsolete
}

func TestDetectNoPatternsReturnEmpty(t *testing.T) {
    // seed no events → all 5 patterns return empty
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/memory/proposal -run "TestDetect" -count=1 -v`
Expected: FAIL

- [ ] **Step 3: 实现 5 patterns**

In `internal/memory/proposal/patterns.go`：

```go
package proposal

import (
    "strings"
    "time"

    "github.com/Scorpio69t/mengdie-code/internal/events"
    "github.com/Scorpio69t/mengdie-code/internal/memory"
)

// DetectRepeatedCorrection: ≥3 user_message 含 "no,", "don't", "wrong", "停止" 类词
func DetectRepeatedCorrection(sessions []ScannedSession) []Proposal {
    ...
}

// DetectRepeatedToolPreference: 单 session 内 edit_file ≥80%
func DetectRepeatedToolPreference(sessions []ScannedSession) []Proposal {
    ...
}

// DetectForgottenTest: shell "go test" failed ≥2 times
func DetectForgottenTest(sessions []ScannedSession) []Proposal {
    ...
}

// DetectCrossSessionPattern: 同 scope + 同 authority 同 claim 出现 ≥3 sessions
func DetectCrossSessionPattern(sessions []ScannedSession, memStore *memory.Store) []Proposal {
    ...
}

// DetectObsoleteClaim: status=stale memories → obsolete proposals
func DetectObsoleteClaim(memStore *memory.Store) []Proposal {
    ...
}
```

每个 pattern 函数返回 `[]Proposal`（命中时 1 条 proposal）。

- [ ] **Step 4: 跑测试确认 pass**

Run: `go test ./internal/memory/proposal -run "TestDetect" -count=1 -v`
Expected: PASS

- [ ] **Step 5: 跑全 proposal 测试**

Run: `go test ./internal/memory/proposal -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/memory/proposal/patterns.go internal/memory/proposal/patterns_test.go
git commit -m "feat(memory): 5 rule-based reflect patterns"
```

---

### Task 4: CLI dispatcher + 4 子命令

**Files:**
- Create: `internal/app/reflect.go`
- Create: `internal/app/reflect_test.go`
- Modify: `internal/app/cli.go`（dispatcher case "reflect"）
- Modify: `internal/app/memory.go`（`exitForStoreError` 加 `ErrProposalNotFound`）

**Interfaces:**
- Consumes: `proposal.Store` (Task 1) + `proposal.Pipeline` (Task 2)
- Produces: 4 子命令 + CLI 报告

- [ ] **Step 1: 写失败测试**

In `internal/app/reflect_test.go`：

```go
func TestReflectRunsAndGeneratesProposals(t *testing.T) {
    // seed 1 session with tool events → run `mengdie reflect` → exit 0, 报告
}

func TestReflectProposalsList(t *testing.T) {
    // seed 1 proposal → run `mengdie reflect proposals` → exit 0, output contains id
}

func TestReflectApproveChangesStatus(t *testing.T) {
    // seed 1 proposal proposed → run `mengdie reflect approve <id>` → status=approved
}

func TestReflectRejectChangesStatus(t *testing.T) {
    // seed 1 proposal proposed → run `mengdie reflect reject <id>` → status=rejected
}
```

- [ ] **Step 2: 跑测试确认 fail**

Run: `go test ./internal/app -run "TestReflect" -count=1 -v`
Expected: FAIL（`runReflect*` undefined）

- [ ] **Step 3: 实现 `runReflect`**

In `internal/app/reflect.go`：

```go
package app

import (
    "context"
    "errors"
    "fmt"
    "io"
    "os"
    "time"

    "github.com/Scorpio69t/mengdie-code/internal/app/..."  // helpers
    "github.com/Scorpio69t/mengdie-code/internal/memory/proposal"
)

func runReflect(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
    flags, common := a.newMemoryFlagSet("mengdie reflect", stderr)
    since := flags.String("since", "7d", "时间窗口")
    maxSessions := flags.Int("max-sessions", 5, "最大 session 数")
    if err := flags.Parse(args); err != nil { return flagExitCode(err) }
    if flags.NArg() != 0 { ... exit 2 ... }

    sinceTime, err := parseSince(*since, a.now())
    if err != nil { ... exit 2 ... }

    pipeline, _, _, code := a.openReflectPipeline(ctx, common)
    if code != ExitOK { return code }
    defer ...

    proposals, err := pipeline.Reflect(ctx, proposal.ReflectOptions{
        Since: sinceTime,
        MaxSessions: *maxSessions,
    })
    if err != nil { return exitForStoreError(err) }

    fmt.Fprintf(stdout, "Generated %d proposals (since %s, %d sessions scanned):\n",
        len(proposals), *since, *maxSessions)
    for _, p := range proposals {
        fmt.Fprintf(stdout, "  %s  %s  %q (confidence %.2f)\n",
            p.ID, p.Kind, p.Title, p.Confidence)
    }
    return ExitOK
}
```

- [ ] **Step 4: 实现 `runReflectProposals`**

镜像 `runMemoryList` 模式：

```go
func runReflectProposals(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
    flags, common := a.newMemoryFlagSet("mengdie reflect proposals", stderr)
    statusFlag := flags.String("status", "", "按 status 过滤")
    kindFlag := flags.String("kind", "", "按 kind 过滤")
    since := flags.String("since", "", "时间窗口")
    jsonOutput := flags.Bool("json", false, "输出 JSON Lines")
    limit := flags.Int("limit", 0, "最大条数")
    if err := flags.Parse(args); err != nil { return flagExitCode(err) }
    if flags.NArg() != 0 { ... exit 2 ... }
    if *statusFlag != "" { /* validate enum */ }
    if *kindFlag != "" { /* validate enum */ }

    store, _, _, code := a.openProposalStore(ctx, common)
    if code != ExitOK { return code }
    defer ...

    list, err := store.List(ctx, proposal.ListQuery{
        Status: proposal.ProposalStatus(*statusFlag),
        Kind: proposal.ProposalKind(*kindFlag),
        Limit: *limit,
    })
    if err != nil { return exitForStoreError(err) }
    return writeReflectProposalsTable(stdout, list, *jsonOutput)
}
```

- [ ] **Step 5: 实现 `runReflectApprove` + `runReflectReject`**

```go
func runReflectApprove(ctx context.Context, args []string, a *App, stdout, stderr io.Writer) int {
    if len(args) != 1 { ... exit 2 ... }
    id := args[0]
    store, _, _, code := a.openProposalStore(ctx, common)
    if code != ExitOK { return code }
    defer ...
    reviewer := os.Getenv("USER")
    if reviewer == "" { reviewer = "mengdie" }
    if err := store.UpdateStatus(ctx, id, proposal.StatusApproved, reviewer); err != nil {
        return exitForStoreError(err)
    }
    fmt.Fprintf(stdout, "approved %s\n", id)
    return ExitOK
}
```

`reject` 同理（`StatusRejected`）。

- [ ] **Step 6: dispatcher 加 case "reflect"**

In `internal/app/cli.go`（或 dispatcher），加：

```go
case "reflect":
    return dispatchReflect(ctx, rest, a, stdout, stderr)
```

`dispatchReflect` 根据 `rest[0]` 路由到 `runReflect` / `runReflectProposals` / `runReflectApprove` / `runReflectReject`。

- [ ] **Step 7: `exitForStoreError` 加 `ErrProposalNotFound`**

In `internal/app/memory.go` 的 `exitForStoreError`：

```go
case errors.Is(err, memoryproposal.ErrProposalNotFound):
    return ExitNotFound
```

> 注意 import：`memoryproposal` 是新 package `github.com/Scorpio69t/mengdie-code/internal/memory/proposal`，按既有 alias 风格取个短名。

- [ ] **Step 8: 跑测试确认 pass**

Run: `go test ./internal/app -run "TestReflect" -count=1 -v`
Expected: PASS

- [ ] **Step 9: 跑全 app 测试确认无回归**

Run: `go test ./internal/app -count=1`
Expected: 全 PASS

- [ ] **Step 10: Commit**

```bash
git add internal/app/reflect.go internal/app/reflect_test.go internal/app/cli.go internal/app/memory.go
git commit -m "feat(cli): mengdie reflect subcommand + 4 actions"
```

---

### Task 5: Trust Set runner 扩展 + 5 新场景

**Files:**
- Modify: `internal/memory/trustset/runner.go`（新 action verbs: `reflect`, `reflect_propose`）
- Modify: `internal/memory/trustset/runner_test.go`（45 → 50 计数）
- Modify: `evals/memory/trust-set-v1.json`（5 新场景）

**Interfaces:**
- Consumes: existing `Action` struct（既有）
- Produces: 50 Trust Set scenarios pass

- [ ] **Step 1: 写 5 新场景 JSON**

Append 到 `evals/memory/trust-set-v1.json` `tasks` array：

```json
{"id": "reflect-scan-since-default", "category": "reflect", "description": "默认 since=7d 跑 Reflect → 至少 0 个 proposals", "setup": {"seed_sessions": ["trustset-reflect-1"]}, "actions": [{"type": "reflect", "scope": "project/mengdie", "since": "7d"}], "expected": {"proposals_min": 0}},
{"id": "reflect-scan-no-recent-sessions", "category": "reflect", "description": "since=1h 且无近期 session → 0 proposals", "setup": {}, "actions": [{"type": "reflect", "scope": "project/mengdie", "since": "1h"}], "expected": {"proposals_min": 0, "proposals_max": 0}},
{"id": "reflect-proposal-memory-upgrade", "category": "reflect", "description": "seed 3 sessions 同 claim → cross-session-pattern 触发 → 1 memory_upgrade", "setup": {"seed_sessions": ["trustset-reflect-1", "trustset-reflect-2", "trustset-reflect-3"], "shared_claim": "项目使用 edit_file 修改文件"}, "actions": [{"type": "reflect", "scope": "project/mengdie"}], "expected": {"proposals_count": 1, "proposal_kind": "memory_upgrade"}},
{"id": "reflect-proposal-obsolete", "category": "reflect", "description": "seed 1 stale memory → obsolete-claim 触发 → 1 obsolete proposal", "setup": {"seed_memories": [{"claim": "过期 claim", "authority": "explicit", "status": "stale", "valid_until": "2026-01-01T00:00:00Z"}]}, "actions": [{"type": "reflect", "scope": "project/mengdie"}], "expected": {"proposals_count": 1, "proposal_kind": "obsolete"}},
{"id": "reflect-approve-promotes-status", "category": "reflect", "description": "approve 后 status=approved + reviewer + reviewed_at 非空", "setup": {"seed_proposals": [{"title": "test", "kind": "memory_upgrade", "status": "proposed"}]}, "actions": [{"type": "reflect_propose", "id": "<seed_proposal_id>"}, {"type": "reflect_approve", "id": "<seed_proposal_id>"}], "expected": {"proposal_status": "approved", "reviewer": "$USER"}}
```

> 字段（`seed_sessions` / `shared_claim` / `proposals_min` / `proposal_kind`）需要 runner 解析。如果某些字段太复杂，v0.1 简化：每个场景只 seed 1 行 memory 或 1 个 session，跑 runner 看 proposals 表。

- [ ] **Step 2: 扩 `Action` 类型加 `type` 字段支持 `reflect` 等**

In `internal/memory/trustset/runner.go`，`Action` struct 加 `Since string` 字段（或 `ReflectOpts struct`）。runner 按 `a.Type` 分发到 `reflectAction(ctx, memStore, proposalStore, a, s)`。

- [ ] **Step 3: 实现 `reflectAction` + `reflectProposeAction` + `reflectApproveAction`**

```go
func reflectAction(ctx context.Context, ps *proposal.Store, ms *memory.Store, ss *session.SQLiteStore, a Action, s Scenario) error {
    opts := proposal.ReflectOptions{
        Since: time.Now().Add(-7 * 24 * time.Hour),  // parse a.Since if non-empty
        MaxSessions: 5,
    }
    p := proposal.New(ss, ms, ps, time.Now)
    proposals, err := p.Reflect(ctx, opts)
    if err != nil { return fmt.Errorf("reflect: %w", err) }
    // 校验 s.Expected.ProposalsMin/Max/Kind/Count 等
    return nil
}

func reflectProposeAction(...) error { ... }  // 直接 insert 一条 proposal（seed 用）
func reflectApproveAction(...) error { ... } // UpdateStatus
func reflectRejectAction(...) error { ... }
```

- [ ] **Step 4: `expected` schema 加 proposals 字段**

In `Expected` struct 加：

```go
ProposalsMin    int
ProposalsMax    int
ProposalCount   int  // exact
ProposalKind   string
ProposalStatus string
```

- [ ] **Step 5: `runner_test.go` 计数 45 → 50**

- [ ] **Step 6: 跑 Trust Set 全 50 场景**

Run: `go test -race ./internal/memory/trustset -count=1 -v 2>&1 | tail -50`
Expected: 50/50 scenarios PASS

- [ ] **Step 7: 跑全 memory + app 测试**

Run: `go test -race ./internal/memory -count=1 && go test -race ./internal/app -count=1`
Expected: 全 PASS

- [ ] **Step 8: Commit**

```bash
git add internal/memory/trustset/runner.go internal/memory/trustset/runner_test.go evals/memory/trust-set-v1.json
git commit -m "feat(trustset): reflect actions + 5 M4 scenarios (45 → 50)"
```

---

### Task 6: Live provider + CI + 文档

**Files:**
- Create: `docs/development/phase-4-slice-01/IMPLEMENTATION_REPORT.md`
- Modify: `README.md`（M4 勾选）
- Modify: `.github/workflows/ci.yml`（验证即可，无需新增 step）

**Interfaces:**
- Consumes: 既有 45 Trust Set 场景 + 5 新 reflect 场景
- Produces: 实施报告 + README 勾选

- [ ] **Step 1: 写实施报告**

Create `docs/development/phase-4-slice-01/IMPLEMENTATION_REPORT.md`，结构镜像 M3 Slice 03：

- 交付范围
- 新增 / 修改文件清单
- 关键设计与守则
- Trust Set 退出门禁（50 场景 baseline 5 指标 + 新增 `proposal_acceptance_rate` v0.1 stub）
- 验证
- Follow-up
- 红线检查

**6 指标 baseline 实测：**
- precision@5 ≥ slice 04 baseline (0.333)
- false_recall_rate = 0
- source_traceability ≥ 0.978
- authority_fidelity = 1
- why_completeness = 1
- **proposal_acceptance_rate** (NEW v0.1 stub): 0/0 = 0 (无 manual accept  数据；v0.2 收集)

- [ ] **Step 2: 改 README**

In `README.md:114`，改：

```markdown
- [x] M4 Slice 01：Reflect/Consolidate v0.1 手动复盘提案（[设计稿](./docs/superpowers/specs/2026-08-26-m4-slice-01-reflect-proposal-design.md)）
```

- [ ] **Step 3: 验证 ci.yml**

`.github/workflows/ci.yml` 已有 `运行 Memory Extractor` step。本切片改动在 `internal/memory/proposal`（新建包）+ `internal/app/reflect.go` + `internal/memory/trustset/` + `evals/`。都在 `go test -race ./...` 与 3 OS test jobs 覆盖范围内。**无需新增 step**。

- [ ] **Step 4: 跑最终质量门禁**

```bash
gofmt -l .
go vet ./...
go test -race ./...                  # 除 Windows pre-existing 外全 PASS
go test -race ./internal/memory -count=1
go test -race ./internal/memory/trustset -run TestRunner -count=1  # 50 场景
go test -race ./internal/memory/extractor -count=1
golangci-lint run ./...
govulncheck@v1.1.4 ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./cmd/...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./cmd/...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/...
```

- [ ] **Step 5: Commit**

```bash
git add docs/development/phase-4-slice-01/IMPLEMENTATION_REPORT.md README.md
git commit -m "docs: M4 Slice 01 implementation report + README"
```

---

## Execution Order

| Order | Task | Output |
|---|---|---|
| 1 | Task 1: migration 010 + ProposalStore | `proposals` 表 + `Store` CRUD |
| 2 | Task 2: Pipeline 5 阶段骨架 | `Pipeline.Reflect` 框架（extract 复用 M3） |
| 3 | Task 3: 5 Reflect patterns | `Detect*` 5 函数 |
| 4 | Task 4: CLI dispatcher + 4 子命令 | `mengdie reflect` + 子命令 |
| 5 | Task 5: Trust Set runner 扩展 + 5 新场景 | 45 → 50 scenarios |
| 6 | Task 6: Live provider + CI + 文档 | IMPLEMENTATION_REPORT + README |

## Final Gates

```bash
gofmt -l .                                          # 0
go vet ./...                                        # clean
go test -race ./...                                 # 除 Windows pre-existing TestShellExecute 外全 PASS
go test -race ./internal/memory -count=1            # memory + proposal + trustset + extractor 全过
go test -race ./internal/agent -count=1
go test -race ./internal/app -count=1               # 含 4 TestReflect* 测试
go test -race ./internal/memory/trustset -run TestRunner -count=1  # 50 场景端到端
golangci-lint run ./...                             # 0 issue
govulncheck@v1.1.4 ./...                            # No vulns
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64  go build ./cmd/...   # OK
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64  go build ./cmd/...   # OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64  go build ./cmd/...   # OK
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64  go build ./cmd/...   # OK
```

## Beads Close

CI 全过、user 审核 merge 后：

```bash
bd close mengdie-z48 --reason="M4 Slice 01 完成：6 task 全部 ship，50 Trust Set 场景 baseline 5 指标 + proposal_acceptance_rate stub 0.0 全在 [0,1]，Reflect v0.1 手动触发 5 阶段流水线 + proposals 表 + 4 CLI 子命令就绪，4 目标构建 + golangci-lint + govulncheck 全过，PR ready-for-review"
```