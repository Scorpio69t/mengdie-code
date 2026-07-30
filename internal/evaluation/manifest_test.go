// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package evaluation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadManifestRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	content := `{
  "schema_version": 1,
  "id": "strict",
  "description": "strict decoding",
  "fixture_root": "fixtures",
  "unknown": true,
  "tasks": [{
    "id": "task",
    "title": "Task",
    "prompt": "Do it",
    "fixture": "task",
    "verify": {"command": ["go", "version"], "timeout": "5s"},
    "baseline": {"expected_exit_code": 0}
  }]
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadManifest(path); err == nil {
		t.Fatal("LoadManifest() error = nil, want unknown field error")
	}
}

func TestResolveFixtureRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := resolveFixture(root, "../outside"); err == nil {
		t.Fatal("resolveFixture() error = nil, want path escape error")
	}
}

func TestRunBaseline(t *testing.T) {
	root := t.TempDir()
	fixtureRoot := filepath.Join(root, "fixtures")
	fixture := filepath.Join(fixtureRoot, "go-version")
	if err := os.MkdirAll(fixture, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		ID:            "runner",
		Description:   "runner smoke",
		FixtureRoot:   "fixtures",
		Tasks: []Task{{
			ID:      "go-version",
			Title:   "Go version",
			Prompt:  "Report the Go version",
			Fixture: "go-version",
			Verify: VerifySpec{
				Command: []string{"go", "version"},
				Timeout: "15s",
			},
			Baseline: BaselineSpec{ExpectedExitCode: 0},
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := RunBaseline(context.Background(), manifestPath)
	if err != nil {
		t.Fatalf("RunBaseline() error = %v", err)
	}
	if !result.Passed || result.PassedTasks != 1 || result.FailedTasks != 0 {
		t.Fatalf("RunBaseline() result = %+v, want one passing task", result)
	}
	if got := result.Tasks[0].ActualExitCode; got != 0 {
		t.Fatalf("ActualExitCode = %d, want 0 on %s", got, runtime.GOOS)
	}
}

func TestLimitWriterTruncatesWithoutShortWrite(t *testing.T) {
	writer := newLimitWriter(4)
	if count, err := writer.Write([]byte("123456")); err != nil || count != 6 {
		t.Fatalf("Write() = (%d, %v), want (6, nil)", count, err)
	}
	if got := writer.String(); got != "1234\n[output truncated]" {
		t.Fatalf("String() = %q", got)
	}
}
