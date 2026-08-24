// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Rules is the deterministic extractor: it turns session events into
// candidate memory rows by pattern-matching against the six triggers
// listed in spec §4. It is the cheap default; LLM is opt-in for the
// cases the rules can't cover (Task 5).
//
// The rule* functions are deliberately pure: they take a []eventRow and
// return []memory.Memory with no I/O, no clock, no randomness. That
// makes them trivially unit-testable (rules_test.go exercises each one
// with synthetic events) and lets Task 4 wire Rules.Extract to the real
// event source without touching the rule logic itself.
package extractor

import (
	"context"
	"strings"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
)

// eventRow is the minimal projection of session events the rules read.
// It is intentionally narrower than *session.SQLiteStore's event table;
// only the columns the spec §4 triggers actually inspect are surfaced.
// Task 4 will populate this projection from the session event log.
type eventRow struct {
	Kind      string    // event kind (e.g. "tool.completed", "run.completed", "run.failed")
	Name      string    // tool name for tool.completed; empty otherwise
	Success   bool      // tool.completed exit status; false for failed runs
	Timestamp time.Time // wall-clock stamp; kept for downstream scoring
	SourceRef string    // shell command text, run-failure category, etc
}

// ruleFunc is the pure signature every rule shares. Implementations
// MUST NOT mutate the input slice and SHOULD return nil (not an empty
// slice) when the trigger does not fire so callers can append cheaply.
type ruleFunc func([]eventRow) []memory.Memory

// Rules is the deterministic Extractor implementation. It is intentionally
// thin in Task 3: the real event source is wired in by Task 4
// (EventReader interface + 重构 loadEvents). Until then Extract returns
// (nil, nil) so the app.Runtime call site stays stable while the
// upstream event surface is being finalised.
type Rules struct {
	// source will be populated by Task 4. Until then it is nil and
	// Extract short-circuits to (nil, nil) per the Task 3 contract.
	source eventSource
}

// eventSource is the read-side hook Task 4 will satisfy with a session-
// backed implementation. Defining it here (and returning nil from
// Task 3) keeps the wiring visible without committing to a session
// import in this file.
type eventSource interface {
	LoadEvents(ctx context.Context, sessionID string) ([]eventRow, error)
}

// NewRules returns a Rules with no event source attached. The Task 4
// wiring (e.g. NewRules(sessionStore)) will replace this constructor;
// for now callers use it to register Rules against the Extractor
// interface in app.Runtime.
func NewRules() *Rules {
	return &Rules{}
}

// Extract returns candidate memory rows for sessionID. The Task 3
// implementation is a placeholder that returns (nil, nil) until Task 4
// supplies the real event source. The nil-error / nil-slice shape lets
// app.Runtime short-circuit cleanly while the extractor is being
// developed and matches the documented "no I/O yet" contract.
func (r *Rules) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error) {
	if r == nil {
		return nil, nil
	}
	if r.source == nil {
		return nil, nil
	}
	_ = ctx
	_ = sessionID
	return nil, nil
}

// allRules returns the canonical, ordered list of rule functions. The
// order is not semantically meaningful today (rules inspect disjoint
// fields) but is pinned for deterministic test output and to make
// regressions in the rule set obvious — TestRulesAllRulesRegistered
// guards the count.
func (r *Rules) allRules() []ruleFunc {
	return []ruleFunc{
		ruleEditFile,
		ruleWriteFile,
		ruleGoTest,
		ruleGoLint,
		ruleRunAllSuccess,
		ruleProviderProtocolFailures,
	}
}

// ruleEditFile: at least one successful tool.completed for edit_file.
// Trigger: kind=tool.completed ∧ name=edit_file ∧ success=true.
// Authority: repository (the project's tool choice is observable from
// the file edits themselves).
func ruleEditFile(events []eventRow) []memory.Memory {
	for _, e := range events {
		if e.Kind == "tool.completed" && e.Name == "edit_file" && e.Success {
			return []memory.Memory{{
				Claim:     "项目使用 edit_file 修改文件",
				Authority: memory.AuthorityRepository,
			}}
		}
	}
	return nil
}

// ruleWriteFile: at least one successful tool.completed for write_file.
// Trigger: kind=tool.completed ∧ name=write_file ∧ success=true.
// Authority: repository.
func ruleWriteFile(events []eventRow) []memory.Memory {
	for _, e := range events {
		if e.Kind == "tool.completed" && e.Name == "write_file" && e.Success {
			return []memory.Memory{{
				Claim:     "项目使用 write_file 创建或覆盖文件",
				Authority: memory.AuthorityRepository,
			}}
		}
	}
	return nil
}

// ruleGoTest: any successful tool.completed shell whose SourceRef
// contains the substring "go test". The SourceRef carries the command
// text per Task 4's event projection; substring matching (rather than
// full-parse) keeps the rule robust to flag variations.
// Trigger: kind=tool.completed ∧ name=shell ∧ success=true ∧
// SourceRef ⊇ "go test".
// Authority: verified (test invocation is a build-verifiable signal).
func ruleGoTest(events []eventRow) []memory.Memory {
	for _, e := range events {
		if e.Kind == "tool.completed" && e.Name == "shell" && e.Success && strings.Contains(e.SourceRef, "go test") {
			return []memory.Memory{{
				Claim:     "项目测试入口是 go test ./...",
				Authority: memory.AuthorityVerified,
			}}
		}
	}
	return nil
}

// ruleGoLint: any successful tool.completed shell whose SourceRef
// contains "golangci-lint". Mirrors ruleGoTest in shape.
// Authority: verified.
func ruleGoLint(events []eventRow) []memory.Memory {
	for _, e := range events {
		if e.Kind == "tool.completed" && e.Name == "shell" && e.Success && strings.Contains(e.SourceRef, "golangci-lint") {
			return []memory.Memory{{
				Claim:     "项目使用 golangci-lint 做静态检查",
				Authority: memory.AuthorityVerified,
			}}
		}
	}
	return nil
}

// ruleRunAllSuccess: at least one run.completed event AND zero failed
// tool.completed events. A single failed tool disqualifies the run
// even when run.completed is present.
// Trigger: ∃ kind=run.completed ∧ ∀ tool.completed, success=true.
// Authority: inferred (the run was successful, but the conclusion is
// the agent's, not the user's).
func ruleRunAllSuccess(events []eventRow) []memory.Memory {
	hasCompleted := false
	for _, e := range events {
		if e.Kind == "run.completed" {
			hasCompleted = true
		}
		if e.Kind == "tool.completed" && !e.Success {
			return nil
		}
	}
	if !hasCompleted {
		return nil
	}
	return []memory.Memory{{
		Claim:     "本次 Agent Run 整体成功",
		Authority: memory.AuthorityInferred,
	}}
}

// ruleProviderProtocolFailures: at least two run.failed events whose
// SourceRef carries category=provider_protocol. Threshold of 2 (not 1)
// avoids a single transient blip flipping the memory; other failure
// categories do not accumulate toward the threshold.
// Trigger: #{run.failed ∧ SourceRef ⊇ "category=provider_protocol"} ≥ 2.
// Authority: inferred.
func ruleProviderProtocolFailures(events []eventRow) []memory.Memory {
	n := 0
	for _, e := range events {
		if e.Kind == "run.failed" && strings.Contains(e.SourceRef, "category=provider_protocol") {
			n++
		}
	}
	if n < 2 {
		return nil
	}
	return []memory.Memory{{
		Claim:     "Provider 协议层不稳定",
		Authority: memory.AuthorityInferred,
	}}
}