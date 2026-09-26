// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/evaluation"
)

func TestRunRejectsUnknownSubcommand(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := run(context.Background(), []string{"unknown"}, io.Discard, stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown subcommand") {
		t.Fatalf("stderr missing unknown subcommand message: %q", stderr.String())
	}
}

func TestRunRejectsBaselinePositionalArguments(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := run(context.Background(), []string{"baseline", "--manifest", "x", "extra"}, io.Discard, stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
}

func TestRunRejectsChaosPositionalArguments(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := run(context.Background(), []string{"chaos", "--manifest", "x", "extra"}, io.Discard, stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
}

func TestRunRepoValidateOutputsRedactedSummary(t *testing.T) {
	manifest := evaluation.RealRepositoryManifest{
		SchemaVersion: 1, ID: "test-pilot", Description: "offline validation",
		Tasks: []evaluation.RealRepositoryTask{{
			ID: "task-1", Title: "Task", SourceURL: "https://github.com/org/repo.git",
			SourceCommit: "2ee0e963942d91be7944dfcc07dc8c02a4a78566", License: "MIT",
			TaskSource: "issue-1", Prompt: "secret-prompt-content",
			Verifier:   evaluation.VerifySpec{Command: []string{"go", "test", "./..."}, Timeout: "1m"},
			Baseline:   evaluation.BaselineSpec{ExpectedExitCode: 1},
			Acceptance: evaluation.RealRepositoryAcceptance{AllowedChanges: []string{"context.go"}},
			RiskBudget: evaluation.RealRepositoryRiskBudget{AllowedEffects: []string{"read", "write", "execute"}, NetworkAccess: "provider_only", MaxToolCalls: 10, MaxCommandCalls: 1},
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := run(context.Background(), []string{"repo", "validate", "--manifest", manifestPath, "--pretty"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "secret-prompt-content") || strings.Contains(stdout.String(), "go test") {
		t.Fatalf("summary leaked prompt or verifier command: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"validation_status": "valid"`) {
		t.Fatalf("summary missing valid status: %s", stdout.String())
	}
}

func TestRunRepoValidateRequiresManifest(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := run(context.Background(), []string{"repo", "validate"}, io.Discard, stderr)
	if code != 2 || !strings.Contains(stderr.String(), "缺少 --manifest") {
		t.Fatalf("run() = (%d, %q), want missing manifest usage error", code, stderr.String())
	}
}

func TestRunRepoBaselineRequiresExplicitLocalExecutionOptIn(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := run(context.Background(), []string{"repo", "baseline", "--manifest", "candidate.json", "--task", "task-1", "--verifier-bin", "/usr/bin/go"}, io.Discard, stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--allow-unisolated") {
		t.Fatalf("run() = (%d, %q), want explicit local execution opt-in", code, stderr.String())
	}
}

func TestRunRepoBaselineRejectsMissingTaskAndPositionalArguments(t *testing.T) {
	for _, args := range [][]string{
		{"repo", "baseline", "--manifest", "candidate.json", "--verifier-bin", "/usr/bin/go", "--allow-unisolated"},
		{"repo", "baseline", "--manifest", "candidate.json", "--task", "task-1", "--verifier-bin", "/usr/bin/go", "--allow-unisolated", "extra"},
	} {
		stderr := &bytes.Buffer{}
		if code := run(context.Background(), args, io.Discard, stderr); code != 2 {
			t.Fatalf("run(%q) = %d, want usage error; stderr = %q", args, code, stderr.String())
		}
	}
}

func TestRunReturnsRunErrorWhenDiagnosticWriteFails(t *testing.T) {
	want := errors.New("diagnostic writer failed")
	code := run(context.Background(), []string{"baseline", "--manifest", "evals/coding/missing.json"}, io.Discard, failingWriter{err: want})
	if code != 1 && code != 2 {
		t.Fatalf("run() code = %d, want 1 or 2", code)
	}
}

type failingWriter struct {
	err error
}

func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
