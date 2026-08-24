// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Rules is the deterministic extractor: it turns session events into
// candidate memory rows by pattern-matching against the six triggers
// listed in spec §4. It is the cheap default; LLM is opt-in for the
// cases the rules can't cover (Task 5).
//
// The rule* functions are deliberately pure: they take a
// []session.EventRow and return []memory.Memory with no I/O, no clock,
// no randomness. That makes them trivially unit-testable
// (rules_test.go exercises each one with synthetic events) and lets
// Rules.Extract wire a real EventReader in Task 4 without touching the
// rule logic itself.
package extractor

import (
	"context"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// rulesLoadLimit caps how many events Rules.Extract asks the reader for
// in one call. The value matches Task 4's brief and is intentionally
// small enough to bound memory use on long-running sessions.
const rulesLoadLimit = 500

// ruleFunc is the pure signature every rule shares. Implementations
// MUST NOT mutate the input slice and SHOULD return nil (not an empty
// slice) when the trigger does not fire so callers can append cheaply.
type ruleFunc func([]session.EventRow) []memory.Memory

// Rules is the deterministic Extractor implementation. It reads events
// through an EventReader (typically a session.SQLiteStore wrapped by
// NewSQLiteReader) and runs the rule set in allRules().
type Rules struct {
	// eventReader is the read-side hook Task 4 wires against
	// session.SQLiteStore. A nil reader makes Extract short-circuit to
	// (nil, nil) so callers can register Rules before the wiring is in
	// place without breaking app.Runtime.
	eventReader EventReader
}

// NewRules returns a Rules bound to the supplied EventReader. Passing
// nil is allowed and keeps the Task 3 placeholder contract alive —
// Extract returns (nil, nil) without error when the reader is missing.
// Production wiring is NewRules(NewSQLiteReader(sessionStore)).
func NewRules(reader EventReader) *Rules {
	return &Rules{eventReader: reader}
}

// Extract loads the session's events through the reader and runs each
// rule in order. The slice returned by the rules is appended into a
// fresh backing array; rules MUST NOT share state across calls.
func (r *Rules) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error) {
	if r == nil || r.eventReader == nil {
		return nil, nil
	}
	events, err := r.eventReader.Events(ctx, sessionID, rulesLoadLimit)
	if err != nil {
		return nil, err
	}
	out := make([]memory.Memory, 0, len(events))
	for _, rule := range r.allRules() {
		out = append(out, rule(events)...)
	}
	return out, nil
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
func ruleEditFile(events []session.EventRow) []memory.Memory {
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
func ruleWriteFile(events []session.EventRow) []memory.Memory {
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
// summary per Task 4's event projection; substring matching (rather
// than full-parse) keeps the rule robust to flag variations.
// Trigger: kind=tool.completed ∧ name=shell ∧ success=true ∧
// SourceRef ⊇ "go test".
// Authority: verified (test invocation is a build-verifiable signal).
func ruleGoTest(events []session.EventRow) []memory.Memory {
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
func ruleGoLint(events []session.EventRow) []memory.Memory {
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
func ruleRunAllSuccess(events []session.EventRow) []memory.Memory {
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
func ruleProviderProtocolFailures(events []session.EventRow) []memory.Memory {
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
