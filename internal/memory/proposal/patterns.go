// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package proposal

// Task 3 populates the bodies of the 5 rule-based Reflect detectors
// (spec §9.3): each one inspects the in-memory ScannedSession list
// assembled by Stage 1 and emits a slice of zero or more Proposal rows
// for Stage 5 to INSERT. v0.1 stubs return nil so the pipeline compiles
// and the Scan → Extract → Verify → Reflect → Propose choreography is
// end-to-end exercisable ahead of the detector logic landing.
//
// The pattern signatures deliberately take only `sessions
// []ScannedSession` — NOT a *memory.Store — to keep this file free of
// any reference into internal/memory and therefore break the
// otherwise-cycle proposal ↔ memory dependency. Detectors that need
// to consult durable memory rows can either fold that lookup into the
// Scan pass (preferred — keeps patterns pure) or accept a narrow
// proposal-local interface when Task 3 lands; the dispatcher in
// pipeline.go is untouched in either case.

func DetectRepeatedCorrection(sessions []ScannedSession) []Proposal {
	return nil
}

func DetectRepeatedToolPreference(sessions []ScannedSession) []Proposal {
	return nil
}

func DetectForgottenTest(sessions []ScannedSession) []Proposal {
	return nil
}

func DetectCrossSessionPattern(sessions []ScannedSession) []Proposal {
	return nil
}

func DetectObsoleteClaim(sessions []ScannedSession) []Proposal {
	return nil
}
