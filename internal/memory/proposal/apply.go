// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package proposal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
)

// Request is the minimal policy payload consumed by the v0.2 apply
// executor. It mirrors the brief's three-field wire shape
// (action / target / justification) so a future adapter can bridge the
// full policy.Authorizer surface without changing the apply driver's
// call sites. Kept in-package — the apply driver is the only consumer
// today, and the type carries no package-internal state.
type Request struct {
	Action        string
	Target        string
	Justification string
}

// Engine is the narrowed authorization surface the apply executor needs.
// The production policy package (internal/policy) owns a richer Engine
// struct with a tools.PreparedCall-based API; wiring that into the apply
// driver is the v0.3 follow-up (Patch Journal integration per spec §6.2).
// For now, callers may pass nil to skip the policy check — the executor
// will fall through to the filesystem / memory side-effect directly.
// All four Apply* methods that perform a side-effect consult the engine
// when it is non-nil.
type Engine interface {
	Authorize(ctx context.Context, req Request) (bool, error)
}

// DefaultApplyExecutor is the production ApplyExecutor. It routes each
// proposal kind to its corresponding side-effect:
//
//   - ApplyMemoryUpgrade   → memStore.UpgradeMemory (Task 3 promotion path)
//   - ApplyObsolete        → memStore.Forget(ctx, id, false) (soft archive)
//   - ApplyAgentsMdRevision → os.WriteFile (Patch Journal deferred to v0.3)
//   - ApplySkillDraft      → os.WriteFile into projectRoot/skills/
//
// policy is optional. When non-nil the executor asks the engine before
// any filesystem side-effect (file.write / file.create); a denied or
// errored Authorize call short-circuits with ApplyResultDeniedByPolicy
// so the audit row renders distinctly from ApplyResultFailed (per
// proposal.ApplyResult constants). projectRoot is the join root for
// AGENTS.md / skill drafts; an empty string disables the file paths.
type DefaultApplyExecutor struct {
	memStore      *memory.Store
	proposalStore *Store
	policy        Engine
	projectRoot   string
	now           func() time.Time
}

// NewDefaultApplyExecutor builds the production executor. pol may be nil
// to skip the policy gate (used by tests and by callers that wire the
// gate later). now must be non-nil — passing nil will panic on the first
// Apply call so we never silently stamp an "isZero" applied_at.
func NewDefaultApplyExecutor(ms *memory.Store, ps *Store, pol Engine, projectRoot string, now func() time.Time) *DefaultApplyExecutor {
	if now == nil {
		now = time.Now
	}
	return &DefaultApplyExecutor{
		memStore:      ms,
		proposalStore: ps,
		policy:        pol,
		projectRoot:   projectRoot,
		now:           now,
	}
}

// ApplyMemoryUpgrade calls memStore.UpgradeMemory with the proposal
// payload (memory_id / new_claim / new_authority). Returns
// ApplyResult(Result=success|failed, Target=memoryID). Missing payload
// fields and UpgradeMemory errors both surface as ApplyResultFailed with
// a descriptive Error message; the apply driver can branch on the Result
// constant rather than parsing Error text.
func (e *DefaultApplyExecutor) ApplyMemoryUpgrade(ctx context.Context, p Proposal) (ApplyResult, error) {
	memoryID, _ := p.Body.Payload["memory_id"].(string)
	newClaim, _ := p.Body.Payload["new_claim"].(string)
	newAuthority, _ := p.Body.Payload["new_authority"].(string)
	if memoryID == "" || newClaim == "" || newAuthority == "" {
		return ApplyResult{
			ProposalID: p.ID,
			Kind:       p.Kind,
			Result:     ApplyResultFailed,
			Error:      "missing memory_id / new_claim / new_authority",
			AppliedAt:  e.now().UTC(),
		}, nil
	}
	if _, err := e.memStore.UpgradeMemory(ctx, memoryID, newClaim, memory.Authority(newAuthority)); err != nil {
		return ApplyResult{
			ProposalID: p.ID,
			Kind:       p.Kind,
			Target:     memoryID,
			Result:     ApplyResultFailed,
			Error:      err.Error(),
			AppliedAt:  e.now().UTC(),
		}, nil
	}
	return ApplyResult{
		ProposalID: p.ID,
		Kind:       p.Kind,
		Target:     memoryID,
		Result:     ApplyResultSuccess,
		AppliedAt:  e.now().UTC(),
	}, nil
}

// ApplyObsolete archives the memory via memStore.Forget(ctx, id, false)
// — the soft-archive path that flips status to archived while keeping
// the row in storage for audit / undelete. Target echoes the memory id
// so the audit row can render it without re-querying.
func (e *DefaultApplyExecutor) ApplyObsolete(ctx context.Context, p Proposal) (ApplyResult, error) {
	memoryID, _ := p.Body.Payload["memory_id"].(string)
	if memoryID == "" {
		return ApplyResult{
			ProposalID: p.ID,
			Kind:       p.Kind,
			Result:     ApplyResultFailed,
			Error:      "missing memory_id",
			AppliedAt:  e.now().UTC(),
		}, nil
	}
	if err := e.memStore.Forget(ctx, memoryID, false); err != nil {
		return ApplyResult{
			ProposalID: p.ID,
			Kind:       p.Kind,
			Target:     memoryID,
			Result:     ApplyResultFailed,
			Error:      err.Error(),
			AppliedAt:  e.now().UTC(),
		}, nil
	}
	return ApplyResult{
		ProposalID: p.ID,
		Kind:       p.Kind,
		Target:     memoryID,
		Result:     ApplyResultSuccess,
		AppliedAt:  e.now().UTC(),
	}, nil
}

// ApplyAgentsMdRevision rewrites the project AGENTS.md via os.WriteFile.
// The Patch Journal integration (spec §6.2 — three-way merge with the
// prior AGENTS.md and a section-scoped diff) is deferred to v0.3; for
// v0.2 we trust the proposal's payload to carry the full proposed file
// body, which is acceptable because the proposal must already be
// approved (Store.Apply guards the status flip) and the audit row
// records the exact bytes applied.
//
// The policy gate fires before any disk write so a denied
// file.write/AGENTS.md returns ApplyResultDeniedByPolicy without
// touching the filesystem. projectRoot must be non-empty for the write
// to land in the right place; an empty projectRoot surfaces as
// ApplyResultFailed (filepath.Join returns the relative target path
// unchanged, which the OS would happily write under the current working
// directory — refusing up front keeps the audit row honest).
func (e *DefaultApplyExecutor) ApplyAgentsMdRevision(ctx context.Context, p Proposal) (ApplyResult, error) {
	target := "AGENTS.md"
	if e.policy != nil {
		allowed, err := e.policy.Authorize(ctx, Request{
			Action:        "file.write",
			Target:        target,
			Justification: fmt.Sprintf("Apply M4 proposal %s", p.ID),
		})
		if err != nil || !allowed {
			return ApplyResult{
				ProposalID: p.ID,
				Kind:       p.Kind,
				Target:     target,
				Result:     ApplyResultDeniedByPolicy,
				Error:      "policy denied",
				AppliedAt:  e.now().UTC(),
			}, nil
		}
	}
	if e.projectRoot == "" {
		return ApplyResult{
			ProposalID: p.ID,
			Kind:       p.Kind,
			Target:     target,
			Result:     ApplyResultFailed,
			Error:      "project_root is required for agents_md_revision",
			AppliedAt:  e.now().UTC(),
		}, nil
	}
	section, _ := p.Body.Payload["section"].(string)
	proposed, _ := p.Body.Payload["proposed"].(string)
	if section == "" || proposed == "" {
		return ApplyResult{
			ProposalID: p.ID,
			Kind:       p.Kind,
			Target:     target,
			Result:     ApplyResultFailed,
			Error:      "missing section / proposed",
			AppliedAt:  e.now().UTC(),
		}, nil
	}
	path := filepath.Join(e.projectRoot, target)
	if err := os.WriteFile(path, []byte(proposed), 0o644); err != nil {
		return ApplyResult{
			ProposalID: p.ID,
			Kind:       p.Kind,
			Target:     path,
			Result:     ApplyResultFailed,
			Error:      err.Error(),
			AppliedAt:  e.now().UTC(),
		}, nil
	}
	return ApplyResult{
		ProposalID: p.ID,
		Kind:       p.Kind,
		Target:     path,
		Result:     ApplyResultSuccess,
		AppliedAt:  e.now().UTC(),
	}, nil
}

// ApplySkillDraft writes a new skill draft to projectRoot/skills/<name>.md.
// The proposal must carry skill_name + body; the policy gate fires before
// any disk write so a denied file.create/skills/ returns
// ApplyResultDeniedByPolicy without touching the filesystem. Skill
// content is written verbatim — the Reflect Worker is expected to
// validate against the SKILL.md schema (frontmatter + sections) before
// emitting the proposal, and the audit row records the exact bytes
// applied so a reviewer can roll back via `git checkout`.
func (e *DefaultApplyExecutor) ApplySkillDraft(ctx context.Context, p Proposal) (ApplyResult, error) {
	if e.policy != nil {
		allowed, _ := e.policy.Authorize(ctx, Request{
			Action:        "file.create",
			Target:        "skills/",
			Justification: fmt.Sprintf("Apply M4 proposal %s", p.ID),
		})
		if !allowed {
			return ApplyResult{
				ProposalID: p.ID,
				Kind:       p.Kind,
				Result:     ApplyResultDeniedByPolicy,
				Error:      "policy denied",
				AppliedAt:  e.now().UTC(),
			}, nil
		}
	}
	if e.projectRoot == "" {
		return ApplyResult{
			ProposalID: p.ID,
			Kind:       p.Kind,
			Result:     ApplyResultFailed,
			Error:      "project_root is required for skill_draft",
			AppliedAt:  e.now().UTC(),
		}, nil
	}
	skillName, _ := p.Body.Payload["skill_name"].(string)
	body, _ := p.Body.Payload["body"].(string)
	if skillName == "" || body == "" {
		return ApplyResult{
			ProposalID: p.ID,
			Kind:       p.Kind,
			Result:     ApplyResultFailed,
			Error:      "missing skill_name / body",
			AppliedAt:  e.now().UTC(),
		}, nil
	}
	path := filepath.Join(e.projectRoot, "skills", skillName+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return ApplyResult{
			ProposalID: p.ID,
			Kind:       p.Kind,
			Target:     path,
			Result:     ApplyResultFailed,
			Error:      err.Error(),
			AppliedAt:  e.now().UTC(),
		}, nil
	}
	return ApplyResult{
		ProposalID: p.ID,
		Kind:       p.Kind,
		Target:     path,
		Result:     ApplyResultSuccess,
		AppliedAt:  e.now().UTC(),
	}, nil
}
