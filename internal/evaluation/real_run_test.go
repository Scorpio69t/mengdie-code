// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package evaluation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

func realDiagnosticFixture(t *testing.T) (RealRepositoryManifest, RealRepositoryRunOptions, realDiagnosticDeps, *[]string) {
	t.Helper()
	manifest, executable := baselineHelperManifest(t, "5s", 1)
	manifest.Tasks[0].Acceptance.AllowedChanges = []string{"context.go"}
	var roots []string
	prepare := func(_ context.Context, manifest RealRepositoryManifest, _ string) (*PreparedRealRepository, error) {
		root, err := os.MkdirTemp(t.TempDir(), "real-run-")
		if err != nil {
			return nil, err
		}
		workspace := filepath.Join(root, "workspace")
		if err := os.Mkdir(workspace, 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(workspace, "context.go"), []byte("before"), 0o600); err != nil {
			return nil, err
		}
		roots = append(roots, workspace)
		return &PreparedRealRepository{Path: workspace, SourceCommit: manifest.Tasks[0].SourceCommit, root: root}, nil
	}
	process := func(_ context.Context, spec platform.ProcessSpec) (platform.ProcessResult, error) {
		content, err := os.ReadFile(filepath.Join(spec.Dir, "context.go"))
		if err != nil {
			return platform.ProcessResult{ExitCode: -1}, err
		}
		_, _ = spec.Stdout.Write([]byte("private verifier output"))
		if string(content) == "before" {
			return platform.ProcessResult{ExitCode: 1, Duration: time.Millisecond}, nil
		}
		return platform.ProcessResult{ExitCode: 0, Duration: time.Millisecond}, nil
	}
	deps := realDiagnosticDeps{prepare: prepare, process: process}
	deps.baseline = func(ctx context.Context, m RealRepositoryManifest, id string, options RealRepositoryBaselineOptions) (RealRepositoryBaselineResult, error) {
		return runRealRepositoryBaseline(ctx, m, id, options, prepare, process, (*PreparedRealRepository).Close)
	}
	options := RealRepositoryRunOptions{VerifierExecutable: executable, AllowAgentEdit: true}
	return manifest, options, deps, &roots
}

func TestRealDiagnosticUsesThreeSeparateCopiesAndRedactedEvidence(t *testing.T) {
	manifest, options, deps, roots := realDiagnosticFixture(t)
	options.Agent = func(_ context.Context, run RealAgentRunOptions) (RealAgentRunEvidence, error) {
		if run.Prompt != manifest.Tasks[0].Prompt || run.Workspace != (*roots)[1] || run.StateDir == "" {
			t.Fatalf("incorrect Agent task binding: %+v", run)
		}
		if err := os.WriteFile(filepath.Join(run.Workspace, "context.go"), []byte("after"), 0o600); err != nil {
			t.Fatal(err)
		}
		return RealAgentRunEvidence{Status: "completed", ToolCalls: 2}, nil
	}
	result, err := runRealRepositoryDiagnostic(context.Background(), manifest, manifest.Tasks[0].ID, options, deps)
	if err != nil || result.Status != "diagnostic_passed" || result.Mode != "local_unisolated" || len(*roots) != 3 {
		t.Fatalf("result=%+v err=%v roots=%v", result, err, *roots)
	}
	if (*roots)[0] == (*roots)[1] || (*roots)[1] == (*roots)[2] || result.Baseline.ExitCode != 1 || result.Final.ExitCode != 0 || len(result.ChangedFiles) != 1 {
		t.Fatalf("copies or verifier result incorrect: %+v", result)
	}
	for _, root := range *roots {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("workspace not cleaned: %s: %v", root, err)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{manifest.Tasks[0].Prompt, "private verifier output", "evaluator-secret-123"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public evidence leaked %q", secret)
		}
	}
}

func TestRealDiagnosticRejectsOutsideChangeBeforeFinalVerifier(t *testing.T) {
	manifest, options, deps, roots := realDiagnosticFixture(t)
	options.Agent = func(_ context.Context, run RealAgentRunOptions) (RealAgentRunEvidence, error) {
		if err := os.WriteFile(filepath.Join(run.Workspace, "outside.txt"), []byte("bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		return RealAgentRunEvidence{Status: "completed"}, nil
	}
	result, err := runRealRepositoryDiagnostic(context.Background(), manifest, manifest.Tasks[0].ID, options, deps)
	if err != nil || result.Status != "policy_violation" || result.Final != nil || len(*roots) != 2 {
		t.Fatalf("result=%+v err=%v roots=%v", result, err, *roots)
	}
}

func TestRealDiagnosticCancellationDoesNotRunFinalVerifier(t *testing.T) {
	manifest, options, deps, roots := realDiagnosticFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	options.Agent = func(_ context.Context, _ RealAgentRunOptions) (RealAgentRunEvidence, error) {
		cancel()
		return RealAgentRunEvidence{Status: "cancelled"}, context.Canceled
	}
	result, err := runRealRepositoryDiagnostic(ctx, manifest, manifest.Tasks[0].ID, options, deps)
	if err != nil || result.Status != "cancelled" || result.Final != nil || len(*roots) != 2 {
		t.Fatalf("result=%+v err=%v roots=%v", result, err, *roots)
	}
}

func TestRealDiagnosticForcedProcessCleanupIsIndeterminate(t *testing.T) {
	manifest, options, deps, roots := realDiagnosticFixture(t)
	options.Agent = func(_ context.Context, _ RealAgentRunOptions) (RealAgentRunEvidence, error) {
		return RealAgentRunEvidence{Status: "completed", ForcedCleanupCount: 1}, nil
	}
	result, err := runRealRepositoryDiagnostic(context.Background(), manifest, manifest.Tasks[0].ID, options, deps)
	if err != nil || result.Status != "indeterminate" || result.Final != nil || len(*roots) != 2 {
		t.Fatalf("result=%+v err=%v roots=%v", result, err, *roots)
	}
}

func TestRealDiagnosticClosesMismatchedPreparedCopy(t *testing.T) {
	manifest, options, deps, roots := realDiagnosticFixture(t)
	prepare := deps.prepare
	deps.prepare = func(ctx context.Context, m RealRepositoryManifest, id string) (*PreparedRealRepository, error) {
		workspace, err := prepare(ctx, m, id)
		if workspace != nil {
			workspace.SourceCommit = strings.Repeat("0", 40)
		}
		return workspace, err
	}
	options.Agent = func(context.Context, RealAgentRunOptions) (RealAgentRunEvidence, error) {
		t.Fatal("Agent started on mismatched source")
		return RealAgentRunEvidence{}, nil
	}
	result, err := runRealRepositoryDiagnostic(context.Background(), manifest, manifest.Tasks[0].ID, options, deps)
	if err != nil || result.Status != "source_prepare_failed" || len(*roots) != 2 {
		t.Fatalf("result=%+v err=%v roots=%v", result, err, *roots)
	}
	for _, root := range *roots {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("mismatched checkout was not cleaned: %s: %v", root, err)
		}
	}
}
