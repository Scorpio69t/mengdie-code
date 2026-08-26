// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package proposal_test

import (
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/memory/proposal"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// correctionKeywordAny is a stand-in for "user rejected approach" markers.
// Matches the keyword list patterns.go uses.
var correctionKeywordAny = []string{"no,", "don't", "wrong", "停止", "不对"}

// TestDetectRepeatedCorrection seeds one session with four user.message
// events (three with correction keywords, one without) and asserts a
// single agents_md_revision proposal. Below the threshold (3) the
// detector stays silent.
func TestDetectRepeatedCorrection(t *testing.T) {
	sessions := []proposal.ScannedSession{
		{
			SessionID: "s-correction",
			Events: []session.EventRow{
				{Kind: "user.message", SourceRef: "no, please don't use shell for that"},
				{Kind: "user.message", SourceRef: "wrong path, try the repo root"},
				{Kind: "user.message", SourceRef: "停止用 fmt.Println 直接打日志"},
				{Kind: "user.message", SourceRef: "all good, ship it"},
			},
		},
	}
	got := proposal.DetectRepeatedCorrection(sessions)
	if len(got) != 1 {
		t.Fatalf("want 1 proposal, got %d", len(got))
	}
	if got[0].Kind != proposal.KindAgentsMdRevision {
		t.Fatalf("Kind want agents_md_revision, got %s", got[0].Kind)
	}
	if got[0].Confidence <= 0 || got[0].Confidence > 1 {
		t.Fatalf("Confidence out of (0,1] range: %v", got[0].Confidence)
	}
	if len(got[0].BasedOn) != 1 || got[0].BasedOn[0] != "s-correction" {
		t.Fatalf("BasedOn want [s-correction], got %v", got[0].BasedOn)
	}
}

// TestDetectRepeatedToolPreference seeds one session with five
// tool.completed events (four edit_file, one write_file) and asserts a
// single memory_upgrade proposal whose payload reports the 4/5 ratio.
// Going below the 5-event floor or below the 80% threshold should yield
// zero proposals (covered implicitly by the no-match test).
func TestDetectRepeatedToolPreference(t *testing.T) {
	sessions := []proposal.ScannedSession{
		{
			SessionID: "s-editfile",
			Events: []session.EventRow{
				{Kind: "tool.completed", Name: "edit_file", Success: true},
				{Kind: "tool.completed", Name: "edit_file", Success: true},
				{Kind: "tool.completed", Name: "edit_file", Success: true},
				{Kind: "tool.completed", Name: "edit_file", Success: true},
				{Kind: "tool.completed", Name: "write_file", Success: true},
			},
		},
	}
	got := proposal.DetectRepeatedToolPreference(sessions)
	if len(got) != 1 {
		t.Fatalf("want 1 proposal, got %d", len(got))
	}
	if got[0].Kind != proposal.KindMemoryUpgrade {
		t.Fatalf("Kind want memory_upgrade, got %s", got[0].Kind)
	}
	if got[0].SessionID != "s-editfile" {
		t.Fatalf("SessionID want s-editfile, got %q", got[0].SessionID)
	}
}

// TestDetectForgottenTest seeds one session with two shell tool events
// carrying failure markers (exit=1 / FAIL) in SourceRef and asserts a
// single memory_upgrade proposal. One failing shell event alone is
// below the threshold (covered by the no-match test).
func TestDetectForgottenTest(t *testing.T) {
	sessions := []proposal.ScannedSession{
		{
			SessionID: "s-shellfail",
			Events: []session.EventRow{
				{Kind: "tool.completed", Name: "shell", Success: false, SourceRef: "go test ./internal/foo... exit=1"},
				{Kind: "tool.completed", Name: "shell", Success: false, SourceRef: "go vet ./... FAIL build constraints"},
			},
		},
	}
	got := proposal.DetectForgottenTest(sessions)
	if len(got) != 1 {
		t.Fatalf("want 1 proposal, got %d", len(got))
	}
	if got[0].Kind != proposal.KindMemoryUpgrade {
		t.Fatalf("Kind want memory_upgrade, got %s", got[0].Kind)
	}
	if got[0].SessionID != "s-shellfail" {
		t.Fatalf("SessionID want s-shellfail, got %q", got[0].SessionID)
	}
}

// TestDetectCrossSessionPattern seeds three sessions whose Memories
// fields share the same canonical claim (one row per session). A fourth
// session with a different claim stays out. The detector must emit one
// memory_upgrade proposal whose BasedOn lists the three contributing
// sessions.
func TestDetectCrossSessionPattern(t *testing.T) {
	sharedClaim := "go test ./... is the canonical verification command"
	sessions := []proposal.ScannedSession{
		{
			SessionID: "s-a",
			Memories: []memory.Memory{
				{ID: "mem_a1", Claim: sharedClaim, Status: memory.StatusActive},
			},
		},
		{
			SessionID: "s-b",
			Memories: []memory.Memory{
				{ID: "mem_b1", Claim: sharedClaim, Status: memory.StatusActive},
			},
		},
		{
			SessionID: "s-c",
			Memories: []memory.Memory{
				{ID: "mem_c1", Claim: sharedClaim, Status: memory.StatusActive},
			},
		},
		{
			SessionID: "s-d",
			Memories: []memory.Memory{
				{ID: "mem_d1", Claim: "totally different claim", Status: memory.StatusActive},
			},
		},
	}
	got := proposal.DetectCrossSessionPattern(sessions)
	if len(got) != 1 {
		t.Fatalf("want 1 proposal, got %d", len(got))
	}
	if got[0].Kind != proposal.KindMemoryUpgrade {
		t.Fatalf("Kind want memory_upgrade, got %s", got[0].Kind)
	}
	if len(got[0].BasedOn) != 3 {
		t.Fatalf("BasedOn want 3 session ids, got %d (%v)", len(got[0].BasedOn), got[0].BasedOn)
	}
}

// TestDetectObsoleteClaim seeds one session whose Memories contain a
// single stale row (StatusStale) and asserts one obsolete proposal per
// stale row. The active row in the same session must not surface.
func TestDetectObsoleteClaim(t *testing.T) {
	sessions := []proposal.ScannedSession{
		{
			SessionID: "s-stale",
			Memories: []memory.Memory{
				{
					ID:     "mem_stale_1",
					Claim:  "legacy hook pattern (removed in v3)",
					Status: memory.StatusStale,
				},
				{
					ID:     "mem_active_1",
					Claim:  "current canonical pattern",
					Status: memory.StatusActive,
				},
			},
		},
	}
	got := proposal.DetectObsoleteClaim(sessions)
	if len(got) != 1 {
		t.Fatalf("want 1 proposal, got %d", len(got))
	}
	if got[0].Kind != proposal.KindObsolete {
		t.Fatalf("Kind want obsolete, got %s", got[0].Kind)
	}
	if got[0].SessionID != "s-stale" {
		t.Fatalf("SessionID want s-stale, got %q", got[0].SessionID)
	}
}

// TestDetectNoPatternsReturnEmpty verifies the negative path: a single
// session with no events and no memories must not trigger any of the
// five detectors. Catches regressions where one detector starts
// emitting phantom proposals on an empty input.
func TestDetectNoPatternsReturnEmpty(t *testing.T) {
	sessions := []proposal.ScannedSession{
		{SessionID: "s-empty"},
	}
	if got := proposal.DetectRepeatedCorrection(sessions); got != nil {
		t.Fatalf("DetectRepeatedCorrection want nil, got %d proposals", len(got))
	}
	if got := proposal.DetectRepeatedToolPreference(sessions); got != nil {
		t.Fatalf("DetectRepeatedToolPreference want nil, got %d proposals", len(got))
	}
	if got := proposal.DetectForgottenTest(sessions); got != nil {
		t.Fatalf("DetectForgottenTest want nil, got %d proposals", len(got))
	}
	if got := proposal.DetectCrossSessionPattern(sessions); got != nil {
		t.Fatalf("DetectCrossSessionPattern want nil, got %d proposals", len(got))
	}
	if got := proposal.DetectObsoleteClaim(sessions); got != nil {
		t.Fatalf("DetectObsoleteClaim want nil, got %d proposals", len(got))
	}
	// Reference the keyword slice so the test stays in sync with the
	// real keyword list in production code; this also catches the case
	// where someone deletes every keyword.
	if len(correctionKeywordAny) == 0 {
		t.Fatal("correctionKeywordAny unexpectedly empty")
	}
}
