// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package chaos

import (
	"context"
	"path/filepath"
	"testing"
)

// TestRunManifestEndToEnd loads evals/chaos/all.json and asserts every
// scenario passes its recovery contract. It exercises the full chaos
// runner, including Agent construction, SQLite persistence, EventStore
// events, EventStore consistency checks, and the verify command.
func TestRunManifestEndToEnd(t *testing.T) {
	manifestPath, err := filepath.Abs(filepath.Join("..", "..", "..", "evals", "chaos", "all.json"))
	if err != nil {
		t.Fatal(err)
	}
	matrix, err := RunManifest(context.Background(), manifestPath, 1, 1)
	if err != nil {
		t.Fatalf("run manifest: %v", err)
	}
	if !matrix.Passed {
		for _, scenario := range matrix.Scenarios {
			if scenario.Passed {
				continue
			}
			t.Logf("scenario %s failed: %s", scenario.ID, scenario.Reason)
			for _, round := range scenario.Rounds {
				t.Logf("  round passed=%v reason=%s first_err=%s verify_exit=%d",
					round.Passed, round.Reason, round.FirstRunError, round.VerifyExitCode)
				for _, obs := range round.Observations {
					t.Logf("    hook=%s fire=%s tool=%s side=%v", obs.Hook, obs.Fire, obs.Tool, obs.SideEffect)
				}
			}
		}
		t.Fatalf("chaos matrix did not pass")
	}
}
