// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package trustset

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// TestRunnerProducesAllMetrics loads evals/memory/trust-set-v1.json, runs
// every scenario through the real Store, computes the 5-metric baseline
// (precision@5, false_recall_rate, source_traceability, authority_fidelity,
// why_completeness), and writes evidence to evidence/memory-trust-v1.json.
//
// Each scenario runs in its own Store (fresh t.TempDir()) so cross-scenario
// contamination (the same-scope same-authority different-claim dispute
// rule from Task 3 fix d21118d) cannot leak between scenarios. This matches
// Trust Set's intent: per-scenario evidence is independent.
func TestRunnerProducesAllMetrics(t *testing.T) {
	manifestPath := locateManifest(t)
	scenarios := loadScenarios(t, manifestPath)
	if len(scenarios) != 30 {
		t.Fatalf("trust-set-v1.json must have 30 scenarios, got %d", len(scenarios))
	}
	results := make([]ScenarioResult, 0, len(scenarios))
	for _, scenario := range scenarios {
		store := openStore(t)
		store2 := memory.OpenMemory(store)
		report, _ := Run(context.Background(), store2, []Scenario{scenario}, "")
		_ = store.Close()
		if len(report.Scenarios) > 0 {
			results = append(results, report.Scenarios[0])
		}
	}
	aggregated := AggregateForTest(time.Now().UTC(), results)

	t.Logf("trust-set baseline: precision@5=%.2f false_recall=%.2f source_trace=%.2f auth_fid=%.2f why_complete=%.2f",
		aggregated.PrecisionAt5, aggregated.FalseRecallRate, aggregated.SourceTraceability,
		aggregated.AuthorityFidelity, aggregated.WhyCompleteness)

	if aggregated.AuthorityFidelity != 1.0 {
		for _, s := range results {
			if !s.Passed {
				t.Logf("FAIL: %s (cat=%s) reason=%s", s.ID, s.Category, s.Reason)
			}
		}
		t.Fatalf("authority fidelity must be 1.0 (no inferred bypasses to active), got %v", aggregated.AuthorityFidelity)
	}

	if aggregated.PrecisionAt5 < 0 || aggregated.PrecisionAt5 > 1 {
		t.Fatalf("precision@5 out of range: %v", aggregated.PrecisionAt5)
	}
	if aggregated.FalseRecallRate < 0 || aggregated.FalseRecallRate > 1 {
		t.Fatalf("false_recall_rate out of range: %v", aggregated.FalseRecallRate)
	}
	if aggregated.SourceTraceability < 0 || aggregated.SourceTraceability > 1 {
		t.Fatalf("source_traceability out of range: %v", aggregated.SourceTraceability)
	}
	if aggregated.AuthorityFidelity != 1.0 {
		t.Fatalf("authority fidelity must be 1.0 (no inferred bypasses to active), got %v", aggregated.AuthorityFidelity)
	}
	if aggregated.WhyCompleteness < 0 || aggregated.WhyCompleteness > 1 {
		t.Fatalf("why completeness out of range: %v", aggregated.WhyCompleteness)
	}

	out := filepath.Join("evidence", "memory-trust-v1.json")
	if err := writeEvidence(out, aggregated); err != nil {
		t.Fatal(err)
	}
	t.Logf("trust-set baseline: precision@5=%.2f false_recall=%.2f source_trace=%.2f auth_fid=%.2f why_complete=%.2f → %s",
		aggregated.PrecisionAt5, aggregated.FalseRecallRate, aggregated.SourceTraceability,
		aggregated.AuthorityFidelity, aggregated.WhyCompleteness, out)
}

func locateManifest(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(filepath.Join(cwd, "..", "..", "..", "evals", "memory", "trust-set-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func loadScenarios(t *testing.T, path string) []Scenario {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest.Tasks
}

func openStore(t *testing.T) *session.SQLiteStore {
	t.Helper()
	dataDir := t.TempDir()
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir:     dataDir,
		ProjectRoot: t.TempDir(),
		Now:         time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func writeEvidence(path string, report Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
