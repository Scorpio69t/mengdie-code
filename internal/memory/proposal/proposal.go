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
