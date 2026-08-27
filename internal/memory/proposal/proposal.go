// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package proposal 实现 M4 Reflect / Consolidate 流水线的复盘提案存储与
// 状态机（status=proposed|approved|rejected）。
//
// Reflect Worker 读取 events / sessions / memory，写入 reflection_proposals
// 表；CLI 暴露 list / approve / reject，但不会自动 apply（apply 是 v0.2 后续
// 任务，本文件只搭好存储层 + 状态机骨架）。
//
// 设计要点：
//   - Kind 是写时闭合的 enum（memory_upgrade / agents_md_revision /
//     skill_draft / obsolete），UI 渲染与后续 Pipeline 分发按 Kind 路由；
//   - Status 走 proposed → approved | rejected 单向迁移，UpdateStatus 是唯一
//     的状态翻转入口；
//   - Body / BasedOn / Evidence 都是 JSON TEXT 列；Store 层负责 marshal /
//     unmarshal，不向调用方暴露原始字符串；
//   - ListQuery 用空字段表示"无约束"，WHERE 子句按需追加。
package proposal

import (
	"context"
	"errors"
	"time"
)

// ProposalKind is the closed set the Reflect Worker emits. The Reflect Worker
// pipeline routes on Kind (memory_upgrade flows through memory.Save*; the
// other three are deferred to v0.2); unknown values are accepted at write
// time only because the Reflect Worker may add new Kinds during v0.1
// experiments — the Store stays wire-agnostic and lets the CLI surface the
// raw value.
type ProposalKind string

// ProposalStatus tracks the review state of a proposal. Status is one-way:
// once approved or rejected it does not return to proposed; the CLI's
// `mengdie reflect propose-again` (deferred to v0.2) would INSERT a fresh
// row rather than rewind an old one.
type ProposalStatus string

const (
	// KindMemoryUpgrade proposes a change to an existing memory row
	// (memory_id + current_claim + proposed_claim payload). Pipeline is
	// responsible for routing through memory.Save / Approve.
	KindMemoryUpgrade ProposalKind = "memory_upgrade"
	// KindAgentsMdRevision proposes an AGENTS.md rewrite; apply is deferred
	// to v0.2 so this Kind stays inert in v0.1.
	KindAgentsMdRevision ProposalKind = "agents_md_revision"
	// KindSkillDraft proposes a new / revised Skill; apply is deferred to
	// v0.2 so this Kind stays inert in v0.1.
	KindSkillDraft ProposalKind = "skill_draft"
	// KindObsolete marks a memory or pattern as ripe for Forget; v0.1 just
	// surfaces the proposal, the CLI / user decides.
	KindObsolete ProposalKind = "obsolete"

	// StatusProposed is the initial state written by Insert. Only proposed
	// rows are visible to the review queue (list --status=proposed).
	StatusProposed ProposalStatus = "proposed"
	// StatusApproved marks the proposal as accepted by a reviewer. Apply is
	// a separate Pipeline step (v0.2); UpdateStatus does not touch memory.
	StatusApproved ProposalStatus = "approved"
	// StatusRejected marks the proposal as declined. Reviewed rows stay in
	// the table for audit but are filtered out by default in list.
	StatusRejected ProposalStatus = "rejected"
)

// ApplyResult values written to proposal_applies.result. The Apply
// executor returns one of these per call so the audit row + CLI
// surface the same vocabulary; ApplyResultFailed and
// ApplyResultDeniedByPolicy cover the two failure modes the Pipeline
// surfaces distinctly (a failed side-effect vs a policy veto).
const (
	// ApplyResultSuccess marks a successful side-effect (memory row
	// patched, AGENTS.md rewritten, etc.).
	ApplyResultSuccess = "success"
	// ApplyResultFailed marks an executor error returned to Apply
	// (network blip, write conflict, etc.). The audit row stores the
	// executor's error message in proposal_applies.error.
	ApplyResultFailed = "failed"
	// ApplyResultDeniedByPolicy marks a deliberate refusal — the
	// executor returned ApplyResultDeniedByPolicy because a guard rule
	// (Trust Set, AGENTS.md section protection, ...) vetoed the side
	// effect. Distinct from ApplyResultFailed so the CLI can render
	// "denied" without parsing the error text.
	ApplyResultDeniedByPolicy = "denied_by_policy"
)

// Sentinel errors returned by Store. Callers use errors.Is to branch on
// validation vs routing failures.
var (
	// ErrProposalNotFound is returned by Get when no row exists for the
	// requested id. The CLI maps this to a distinct exit code so an
	// approve / reject on an unknown id never silently no-ops.
	ErrProposalNotFound = errors.New("proposal not found")
	// ErrInvalidProposal is returned by Insert when Kind / Title is empty
	// or JSON marshalling fails. Validation up front so the row never lands
	// half-populated.
	ErrInvalidProposal = errors.New("invalid proposal")
	// ErrProposalNotApplicable is returned by Apply when the proposal's
	// Status is not StatusApproved, or when the Kind is unknown to the
	// apply dispatcher. The executor is never invoked on this branch.
	ErrProposalNotApplicable = errors.New("proposal is not applicable")
	// ErrProposalAlreadyApplied is reserved for callers that want to
	// differentiate "the apply driver re-returned the existing record"
	// (the idempotent guard) from "a fresh apply ran end to end".
	// Store.Apply currently returns the existing ApplyResult without
	// wrapping this sentinel; the CLI (Task 5) may use it for an
	// explicit "already applied" exit code.
	ErrProposalAlreadyApplied = errors.New("proposal already applied")
)

// Evidence is a single corroborating signal the Reflect Worker attached to
// the proposal. The v0.1 Reflect Worker emits the same {kind, description,
// confidence} triple the memory package already understands, so future
// cross-package deduplication (Task 5) is mechanical.
type Evidence struct {
	Kind        string  `json:"kind"`
	Description string  `json:"description"`
	Confidence  float64 `json:"confidence"`
}

// ProposalBody is the Kind-specific payload. The Reflect Worker fills
// Payload with whatever the target Kind needs (memory_upgrade writes
// {memory_id, current_claim, proposed_claim}; agents_md_revision writes the
// proposed section, etc.). The Store keeps it opaque — apply / dispatch
// (Task 2 / 3) reads Payload by Kind and is responsible for its own
// schema.
type ProposalBody struct {
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
}

// Proposal is the durable shape of a reflection_proposals row. The JSON
// columns (Body / BasedOn / Evidence) are stored as TEXT in SQLite; the
// Store layer marshals on write and unmarshals on read.
type Proposal struct {
	ID         string
	Kind       ProposalKind
	Title      string
	Body       ProposalBody
	Status     ProposalStatus
	BasedOn    []string
	SessionID  string
	Confidence float64
	Evidence   []Evidence
	ObservedAt time.Time
	ReviewedAt *time.Time
	Reviewer   string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ListQuery is the filter / pagination surface for Store.List. Empty fields
// mean "no constraint on that column" so the dynamic WHERE clause only adds
// filters the caller actually supplied. Limit defaults to listDefaultLimit
// when zero and is capped at listMaxLimit; values outside the range produce
// ErrInvalidQuery so callers can branch on it with errors.Is.
//
// OrderBy is reserved for future sort hints; v0.1 always uses observed_at
// DESC so the queue is newest-first.
type ListQuery struct {
	Status    ProposalStatus
	Kind      ProposalKind
	SessionID string
	Since     time.Time
	Limit     int
	OrderBy   string
}

// ApplyResult is the durable shape of a proposal_applies row. The Store
// stamps ProposalID / Kind / AppliedAt when the executor leaves them
// empty so every audit row carries the provenance even if the
// executor only filled in Result / Target / PatchID / Error. JSON
// tags match the migration's column shape so future CLI / API
// surfaces can stream the row directly.
type ApplyResult struct {
	ProposalID string       `json:"proposal_id"`
	Kind       ProposalKind `json:"kind"`
	Target     string       `json:"target"`
	Result     string       `json:"result"`
	Error      string       `json:"error,omitempty"`
	AppliedAt  time.Time    `json:"applied_at"`
	PatchID    string       `json:"patch_id,omitempty"`
}

// ApplyExecutor is the kind-routed dispatcher Store.Apply calls into.
// One method per ProposalKind so the apply driver can route without
// inspecting Payload — concrete executors (slice 03) live behind the
// interface and own their own side effects (memory.Save,
// AGENTS.md rewrite, Skill draft write, Forget). Unknown Kind values
// cause Store.Apply to return ErrProposalNotApplicable before any
// method is called.
type ApplyExecutor interface {
	ApplyMemoryUpgrade(ctx context.Context, p Proposal) (ApplyResult, error)
	ApplyAgentsMdRevision(ctx context.Context, p Proposal) (ApplyResult, error)
	ApplySkillDraft(ctx context.Context, p Proposal) (ApplyResult, error)
	ApplyObsolete(ctx context.Context, p Proposal) (ApplyResult, error)
}
