// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package proposal

import (
	"fmt"
	"strings"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
)

// kindUserMessage is the project-internal event kind for user chat input.
// "user.message" is not a constant in internal/events (the events package
// only covers system events like tool.completed / run.started); it is a
// project payload kind that survives into the events table through the
// extractor / Trust Set runner. Treated as a plain string for v0.1.
const kindUserMessage = "user.message"

// kindToolCompleted / kindShell are the system-event kinds the tool and
// forgotten-test detectors filter on. Pulled out of string literals so
// typos surface at compile time.
const (
	kindToolCompleted = "tool.completed"
	kindShell         = "shell"
	kindEditFile      = "edit_file"
)

// correctionKeywords are markers for the user rejecting an approach.
// v0.1 covers English + 中文 so both kinds of sessions trip the
// detector. Detection uses a case-insensitive substring match (see
// matchesCorrectionKeyword) so minor wording variations ("don't" /
// "do not") still hit.
var correctionKeywords = []string{
	"no,",
	"don't",
	"do not",
	"wrong",
	"stop",
	"停止",
	"不对",
	"不要",
	"错了",
}

// shellFailureMarkers are the substrings DetectForgottenTest looks for
// in shell tool.completed events' SourceRef. The SourceRef column is
// the only durable carrier for tool summary text on the session row
// projection; v0.1 just matches the cheapest signal — exit code 1 or
// the FAIL token in the summary.
var shellFailureMarkers = []string{"exit=1", "FAIL"}

// DetectRepeatedCorrection scans every user.message event across the
// scanned sessions and emits a single agents_md_revision proposal when
// 3 or more of them carry a correction keyword. One proposal per
// invocation regardless of how many sessions contributed — the point
// of the rule is "user has pushed back on this enough times that the
// system behaviour is wrong", so a single revision suggestion suffices.
//
// BasedOn carries every contributing session id so the review queue
// can pull the raw message stream back if a reviewer wants to verify
// the pattern by hand.
func DetectRepeatedCorrection(sessions []ScannedSession) []Proposal {
	const minCorrections = 3

	count := 0
	var contributing []string
	for _, s := range sessions {
		var sessionHit bool
		for _, ev := range s.Events {
			if ev.Kind != kindUserMessage {
				continue
			}
			if matchesCorrectionKeyword(ev.SourceRef) || matchesCorrectionKeyword(ev.Name) {
				count++
				sessionHit = true
			}
		}
		if sessionHit {
			contributing = append(contributing, s.SessionID)
		}
	}
	if count < minCorrections {
		return nil
	}
	return []Proposal{{
		Kind:  KindAgentsMdRevision,
		Title: "修订 AGENTS.md：用户多次纠正同类做法",
		Body: ProposalBody{
			Kind: string(KindAgentsMdRevision),
			Payload: map[string]any{
				"section": "## User Constraints",
				"reason":  fmt.Sprintf("观察到 %d 条用户纠正类消息", count),
			},
		},
		Confidence: 0.6,
		BasedOn:    contributing,
		ObservedAt: time.Now(),
	}}
}

// DetectRepeatedToolPreference emits one memory_upgrade proposal per
// session whose tool.completed events are dominated by edit_file (≥5
// tool events with edit_file ≥ 80%). The payload carries the exact
// ratio so a downstream memory.Save can persist the fingerprint as a
// verified tool-preference claim.
//
// Sessions below the floor (5 tool events) or below the ratio
// threshold (4/5) stay silent — small samples would produce noisy
// proposals that compete with higher-confidence signals in the review
// queue.
func DetectRepeatedToolPreference(sessions []ScannedSession) []Proposal {
	const (
		minToolEvents = 5
		ratioNum      = 4 // edit_file ratio ≥ 4/5 ⇒ editFile*5 ≥ total*4
		ratioDen      = 5
	)
	var proposals []Proposal
	for _, s := range sessions {
		editFile, totalTool := 0, 0
		for _, ev := range s.Events {
			if ev.Kind != kindToolCompleted {
				continue
			}
			totalTool++
			if ev.Name == kindEditFile {
				editFile++
			}
		}
		if totalTool < minToolEvents || editFile*ratioDen < totalTool*ratioNum {
			continue
		}
		proposals = append(proposals, Proposal{
			Kind:  KindMemoryUpgrade,
			Title: fmt.Sprintf("升级 edit_file fingerprint claim: session=%s (edit_file %d/%d)", s.SessionID, editFile, totalTool),
			Body: ProposalBody{
				Kind: string(KindMemoryUpgrade),
				Payload: map[string]any{
					"session_id":      s.SessionID,
					"edit_file_ratio": float64(editFile) / float64(totalTool),
				},
			},
			SessionID:  s.SessionID,
			Confidence: 0.7,
			BasedOn:    []string{s.SessionID},
			ObservedAt: time.Now(),
		})
	}
	return proposals
}

// DetectForgottenTest emits one memory_upgrade proposal per session
// where 2 or more shell tool.completed events carry a failure marker
// (exit=1 or FAIL) in their SourceRef. v0.1 deliberately stays loose
// — it surfaces a "consider running go test" suggestion, not an exact
// command; the reviewer decides whether to bake it into memory.
func DetectForgottenTest(sessions []ScannedSession) []Proposal {
	const minFailures = 2

	var proposals []Proposal
	for _, s := range sessions {
		failCount := 0
		for _, ev := range s.Events {
			if ev.Kind != kindToolCompleted || ev.Name != kindShell {
				continue
			}
			if matchesAnyMarker(ev.SourceRef, shellFailureMarkers) {
				failCount++
			}
		}
		if failCount < minFailures {
			continue
		}
		proposals = append(proposals, Proposal{
			Kind:  KindMemoryUpgrade,
			Title: fmt.Sprintf("Session %s 测试反复失败 (%d 次)", s.SessionID, failCount),
			Body: ProposalBody{
				Kind: string(KindMemoryUpgrade),
				Payload: map[string]any{
					"session_id":     s.SessionID,
					"fail_count":     failCount,
					"recommendation": "考虑运行 go test ./... 验证修复",
				},
			},
			SessionID:  s.SessionID,
			Confidence: 0.8,
			BasedOn:    []string{s.SessionID},
			ObservedAt: time.Now(),
		})
	}
	return proposals
}

// DetectCrossSessionPattern reads sessions[i].Memories (pre-fetched by
// Pipeline.extract), groups memories by canonical claim via
// memory.CanonicalizeClaim, and emits one memory_upgrade proposal per
// claim observed in 3 or more distinct sessions. Sessions are deduped
// per claim so a single noisy session with 5 same-claim memories does
// not falsely trigger the threshold.
//
// memory.CanonicalizeClaim is the single source of truth for claim
// equality (per memory package's §4.2 invariant); reusing it keeps
// detector-level claim equality aligned with Store.Save idempotency,
// so a detector finding here and a later dedup-insert there see
// exactly the same string.
func DetectCrossSessionPattern(sessions []ScannedSession) []Proposal {
	const minSessions = 3

	claimSessions := map[string]map[string]struct{}{}
	for _, s := range sessions {
		for _, m := range s.Memories {
			key := memory.CanonicalizeClaim(m.Claim)
			if key == "" {
				continue
			}
			if claimSessions[key] == nil {
				claimSessions[key] = map[string]struct{}{}
			}
			claimSessions[key][s.SessionID] = struct{}{}
		}
	}
	var proposals []Proposal
	for claim, set := range claimSessions {
		if len(set) < minSessions {
			continue
		}
		sids := make([]string, 0, len(set))
		for sid := range set {
			sids = append(sids, sid)
		}
		proposals = append(proposals, Proposal{
			Kind:  KindMemoryUpgrade,
			Title: fmt.Sprintf("升级记忆：%s (跨 %d 个 session 重复)", truncateRunes(claim, 50), len(sids)),
			Body: ProposalBody{
				Kind: string(KindMemoryUpgrade),
				Payload: map[string]any{
					"claim":          claim,
					"session_count":  len(sids),
					"sessions":       sids,
					"recommendation": "升级为显式 memory (authority=verified 或 repository)",
				},
			},
			BasedOn:    sids,
			Confidence: 0.75,
			ObservedAt: time.Now(),
		})
	}
	return proposals
}

// DetectObsoleteClaim walks sessions[i].Memories and emits one
// obsolete proposal per memory with Status == memory.StatusStale. The
// detector is intentionally quiet about non-stale rows (active /
// proposed / superseded) so the review queue only sees entries the
// pipeline believes are ripe for Forget.
func DetectObsoleteClaim(sessions []ScannedSession) []Proposal {
	var proposals []Proposal
	for _, s := range sessions {
		for _, m := range s.Memories {
			if m.Status != memory.StatusStale {
				continue
			}
			proposals = append(proposals, Proposal{
				Kind:  KindObsolete,
				Title: fmt.Sprintf("过期 claim: %s", truncateRunes(m.Claim, 50)),
				Body: ProposalBody{
					Kind: string(KindObsolete),
					Payload: map[string]any{
						"memory_id": m.ID,
						"claim":     m.Claim,
						"reason":    "valid_until 已过期，建议归档或 supersede",
					},
				},
				BasedOn:    []string{m.ID},
				SessionID:  s.SessionID,
				Confidence: 0.9,
				ObservedAt: time.Now(),
			})
		}
	}
	return proposals
}

// matchesCorrectionKeyword reports whether text contains any keyword
// in correctionKeywords, case-insensitively. Substring match is the
// cheapest signal that survives minor wording variations.
func matchesCorrectionKeyword(text string) bool {
	lower := strings.ToLower(text)
	for _, kw := range correctionKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// matchesAnyMarker reports whether text contains any of markers as a
// substring. Case-sensitive: "FAIL" is the canonical Go test failure
// token and "exit=1" is the SourceRef-shaped exit-code carrier, both
// case-sensitive by convention.
func matchesAnyMarker(text string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// truncateRunes returns s truncated to at most max runes, with "..."
// appended when truncation happened. Rune-aware so multi-byte
// characters (CJK) are not split mid-codepoint.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
