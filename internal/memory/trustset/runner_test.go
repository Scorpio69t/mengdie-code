// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package trustset

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
//
// M3 Slice 02 Task 9 extended the manifest to 35 scenarios (30 slice-01 +
// 5 inferred-extraction scenarios). M3 Slice 03 Task 5 adds 5 more
// (auto-approved-rules-* / auto-approved-llm-fingerprint /
// auto-approved-llm-non-fingerprint), bringing the total to 40. M3
// Slice 04 Task 5 adds another 5 (cross-authority-{explicit,verified,
// repository}-vs-inferred + auto-approve-{skipped-cross-authority,
// still-runs-no-conflict}), bringing the total to 45. The slice-03
// scenarios exercise the M3 Slice 03 fingerprint auto-Approve path via
// expected.extracted_memories[].status = "auto-approved" (which
// expectedMatches translates to "matches any status=active candidate");
// auto-approved-llm-non-fingerprint is the negative test that asserts a
// non-fingerprint claim stays at status=proposed. The slice-04 scenarios
// exercise the M3 Slice 04 cross-authority guard in extractAction: the
// first three cross-authority-* scenarios seed a higher-authority peer
// and assert the inferred candidate lands at status=disputed instead of
// being auto-Approved; auto-approve-skipped-cross-authority is the
// explicit-vs-inferred pair; auto-approve-still-runs-no-conflict is the
// regression test that proves the guard does not over-block a clean
// fingerprint candidate.
func TestRunnerProducesAllMetrics(t *testing.T) {
	manifestPath := locateManifest(t)
	scenarios := loadScenarios(t, manifestPath)
	if len(scenarios) != 45 {
		t.Fatalf("trust-set-v1.json must have 45 scenarios, got %d", len(scenarios))
	}
	results := make([]ScenarioResult, 0, len(scenarios))
	for _, scenario := range scenarios {
		store := openStore(t)
		report, _ := Run(context.Background(), store, []Scenario{scenario}, "")
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

// TestRunnerHandlesExtractScenario pins the contract for the new extract
// action verb introduced by M3 Slice 02 Task 9: the runner must (a) recognise
// the `run_run` + `extract` action pair, (b) route extracted candidates
// through ProposeMemory (so they land as authority=inferred, status=proposed
// regardless of which Extractor stamped the candidate), and (c) match the
// expected extracted_memories[] list by claim_contains + authority + status.
//
// This test targets extractor-rules-edits because it is the simplest case
// (deterministic Rules extractor, single tool.completed event) and therefore
// the cleanest regression signal if the runner wiring drifts. Scenarios 4
// and 5 (LLM stub / Hybrid) get their own coverage via the existing 35-scenario
// TestRunnerProducesAllMetrics loop below.
func TestRunnerHandlesExtractScenario(t *testing.T) {
	manifestPath := locateManifest(t)
	scenarios := loadScenarios(t, manifestPath)
	var target *Scenario
	for i := range scenarios {
		if scenarios[i].ID == "extractor-rules-edits" {
			target = &scenarios[i]
			break
		}
	}
	if target == nil {
		t.Fatal("extractor-rules-edits scenario not found in trust-set-v1.json")
	}
	store := openStore(t)
	defer func() { _ = store.Close() }()
	report, runErr := Run(context.Background(), store, []Scenario{*target}, "")
	if runErr != nil {
		t.Fatalf("Run returned error: %v", runErr)
	}
	if len(report.Scenarios) == 0 {
		t.Fatal("no scenario results")
	}
	got := report.Scenarios[0]
	if !got.Passed {
		if strings.Contains(got.Reason, "unknown action type") {
			t.Fatalf("runner does not yet support extract / run_run actions: %s", got.Reason)
		}
		t.Fatalf("scenario %s failed: %s", got.ID, got.Reason)
	}
}
