// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package evaluation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validRealManifest() RealRepositoryManifest {
	return RealRepositoryManifest{
		SchemaVersion: 1,
		ID:            "real-pilot",
		Description:   "Public repository pilot manifest",
		Tasks: []RealRepositoryTask{{
			ID:           "gin-2121",
			Title:        "Handle nil headers",
			SourceURL:    "https://github.com/gin-gonic/gin.git",
			SourceCommit: "2ee0e963942d91be7944dfcc07dc8c02a4a78566",
			License:      "MIT",
			TaskSource:   "Gin PR #2121",
			Prompt:       "Fix the panic without leaking the evaluator token evaluator-secret-123.",
			Verifier:     VerifySpec{Command: []string{"go", "test", "./..."}, Timeout: "10m"},
			Baseline:     BaselineSpec{ExpectedExitCode: 1},
			Acceptance:   RealRepositoryAcceptance{AllowedChanges: []string{"context.go", "context_test.go"}},
			RiskBudget: RealRepositoryRiskBudget{
				AllowedEffects: []string{"read", "write", "execute"}, NetworkAccess: "provider_only",
				MaxToolCalls: 80, MaxCommandCalls: 2,
			},
		}},
	}
}

func TestRealRepositoryManifestValidate(t *testing.T) {
	if err := validRealManifest().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRealRepositoryManifestRejectsUnsafeOrIncompleteFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RealRepositoryTask)
		want   string
	}{
		{name: "missing license", mutate: func(task *RealRepositoryTask) { task.License = " " }, want: "license is required"},
		{name: "short commit", mutate: func(task *RealRepositoryTask) { task.SourceCommit = "deadbeef" }, want: "full lowercase"},
		{name: "credentials in source url", mutate: func(task *RealRepositoryTask) { task.SourceURL = "https://token:secret@github.com/org/repo" }, want: "credentials"},
		{name: "local source url", mutate: func(task *RealRepositoryTask) { task.SourceURL = "https://127.0.0.1/repo" }, want: "private or local"},
		{name: "shell verifier", mutate: func(task *RealRepositoryTask) { task.Verifier.Command = []string{"sh", "-c", "go test ./..."} }, want: "not a shell"},
		{name: "windows shell verifier", mutate: func(task *RealRepositoryTask) {
			task.Verifier.Command = []string{`C:\Windows\System32\cmd.exe`, "/c", "go test ./..."}
		}, want: "not a shell"},
		{name: "absolute allow path", mutate: func(task *RealRepositoryTask) { task.Acceptance.AllowedChanges = []string{"/etc/passwd"} }, want: "workspace-relative"},
		{name: "wildcard allow path", mutate: func(task *RealRepositoryTask) { task.Acceptance.AllowedChanges = []string{"**"} }, want: "exact, non-wildcard"},
		{name: "broad root allow path", mutate: func(task *RealRepositoryTask) { task.Acceptance.AllowedChanges = []string{"."} }, want: "exact, non-wildcard"},
		{name: "missing risk budget", mutate: func(task *RealRepositoryTask) { task.RiskBudget = RealRepositoryRiskBudget{} }, want: "allowed_effects is required"},
		{name: "unbounded commands", mutate: func(task *RealRepositoryTask) { task.RiskBudget.MaxCommandCalls = 81 }, want: "max_command_calls"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validRealManifest()
			test.mutate(&manifest.Tasks[0])
			err := manifest.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestLoadRealRepositoryManifestRequiresVerifierArgvArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	content := `{"schema_version":1,"id":"test","description":"test","tasks":[{"id":"one","title":"One","source_url":"https://github.com/org/repo","source_commit":"2ee0e963942d91be7944dfcc07dc8c02a4a78566","license":"MIT","task_source":"issue","prompt":"task","verifier":{"command":"go test ./...","timeout":"1m"},"baseline":{"expected_exit_code":1},"acceptance":{"allowed_changes":["file.go"]},"risk_budget":{"allowed_effects":["read"],"network_access":"disabled","max_tool_calls":1,"max_command_calls":0}}]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRealRepositoryManifest(path); err == nil {
		t.Fatal("LoadRealRepositoryManifest() error = nil, want command string type error")
	}
}

func TestRealRepositorySummaryOmitsPromptsAndVerifierArguments(t *testing.T) {
	manifest := validRealManifest()
	var output bytes.Buffer
	if err := EncodeRealRepositorySummary(&output, manifest, true); err != nil {
		t.Fatalf("EncodeRealRepositorySummary() error = %v", err)
	}
	if strings.Contains(output.String(), "evaluator-secret-123") || strings.Contains(output.String(), "go test") {
		t.Fatalf("summary leaked prompt or verifier argument: %s", output.String())
	}
	var summary RealRepositoryManifestSummary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatalf("summary is invalid JSON: %v", err)
	}
	if summary.TaskCount != 1 || len(summary.Tasks) != 1 || summary.Tasks[0].ValidationStatus != "valid" {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestRealRepositoryManifestRejectsDuplicateTaskIDs(t *testing.T) {
	manifest := validRealManifest()
	manifest.Tasks = append(manifest.Tasks, manifest.Tasks[0])
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Validate() error = %v, want duplicate task id error", err)
	}
}
