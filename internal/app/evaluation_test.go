// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/config"
	"github.com/Scorpio69t/mengdie-code/internal/evaluation"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func TestEvaluationIgnoresProjectProviderConfigAndDeniesUnapprovedEdit(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "value.txt"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAppConfig(t, workspace, `
[profiles.default]
provider = "openai-compatible"
base_url = "https://untrusted.example/v1"
model = "untrusted-model"

[approval]
allow_commands = ["go test"]
`)
	application, _, _ := newTestApp(t, nil)
	userPath := filepath.Join(application.userConfigDir, "mengdie", "config.toml")
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(`
[profiles.default]
provider = "openai-compatible"
base_url = "https://trusted.example/v1"
model = "trusted-model"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &appFakeProvider{responses: []*provider.ChatResponse{
		appToolResponse("edit", "write_file", map[string]any{"path": "value.txt", "content": "after", "overwrite": true}),
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}
	var providerURL string
	application.newProvider = func(profile config.Profile, _ string) (provider.Provider, error) {
		providerURL = profile.BaseURL
		return fake, nil
	}
	result, err := application.RunEvaluation(context.Background(), EvaluationRunOptions{
		Workspace: workspace, StateDir: stateDir, Prompt: "change value", Policy: EvaluationPolicy{
			AllowedEffects: []tools.Effect{tools.EffectRead, tools.EffectWrite, tools.EffectExecute},
			AllowedChanges: []string{"value.txt"},
			MaxToolCalls:   3, MaxCommandCalls: 1,
		},
	})
	if err != nil || result.Status != "policy_denied" || result.ToolCalls != 1 || result.DeniedTools != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if providerURL != "https://trusted.example/v1" {
		t.Fatalf("untrusted project changed Provider URL: %s", providerURL)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "value.txt"))
	if err != nil || string(content) != "before" {
		t.Fatalf("unapproved edit changed file: %q err=%v", content, err)
	}
	if len(fake.requests) == 0 {
		t.Fatal("Provider not called")
	}
	for _, tool := range fake.requests[0].Tools {
		if strings.Contains(tool.Function.Name, "memory") || strings.Contains(tool.Function.Name, "skill") {
			t.Fatalf("evaluation advertised unrelated tool %q", tool.Function.Name)
		}
	}
}

func TestEvaluationRejectsWriteOutsideExactPathBeforeExecution(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "outside.txt"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	application, _, _ := newTestApp(t, nil)
	userPath := filepath.Join(application.userConfigDir, "mengdie", "config.toml")
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte("[profiles.default]\nprovider = \"openai-compatible\"\nbase_url = \"https://trusted.example/v1\"\nmodel = \"trusted-model\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &appFakeProvider{responses: []*provider.ChatResponse{
		appToolResponse("outside", "write_file", map[string]any{"path": "outside.txt", "content": "after", "overwrite": true}),
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}
	application.newProvider = func(config.Profile, string) (provider.Provider, error) { return fake, nil }
	result, err := application.RunEvaluation(context.Background(), EvaluationRunOptions{
		Workspace: workspace, StateDir: stateDir, Prompt: "change", Policy: EvaluationPolicy{
			AllowedEffects: []tools.Effect{tools.EffectRead, tools.EffectWrite}, AllowedChanges: []string{"allowed.txt"},
			AllowEdit: true, MaxToolCalls: 2,
		},
	})
	if err != nil || result.Status != "policy_denied" || result.DeniedTools != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "outside.txt"))
	if err != nil || string(content) != "before" {
		t.Fatalf("outside file changed: %q err=%v", content, err)
	}
}

func TestEvaluationDoesNotInheritProfileCommandApproval(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.com/eval\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	goBinary, err = filepath.Abs(goBinary)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := evaluation.RealAgentEnvironment(goBinary, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	application, _, _ := newTestApp(t, nil)
	userPath := filepath.Join(application.userConfigDir, "mengdie", "config.toml")
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte("[profiles.default]\nprovider = \"openai-compatible\"\nbase_url = \"https://trusted.example/v1\"\nmodel = \"trusted-model\"\n[approval]\nallow_commands = [\"go test\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &appFakeProvider{responses: []*provider.ChatResponse{
		appToolResponse("shell", "shell", map[string]any{"command": "go test ./..."}),
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}
	application.newProvider = func(config.Profile, string) (provider.Provider, error) { return fake, nil }
	result, err := application.RunEvaluation(context.Background(), EvaluationRunOptions{
		Workspace: workspace, StateDir: stateDir, Prompt: "test", Environment: environment,
		Policy: EvaluationPolicy{AllowedEffects: []tools.Effect{tools.EffectRead, tools.EffectExecute}, MaxToolCalls: 2, MaxCommandCalls: 1},
	})
	if err != nil || result.Status != "policy_denied" || result.CommandCalls != 1 || result.DeniedTools != 1 ||
		len(fake.requests) != 2 || !evaluationRequestContains(fake.requests[1], `"category":"denied"`) {
		t.Fatalf("profile command bypassed evaluation policy: result=%+v err=%v", result, err)
	}
}

func TestEvaluationCompletesReadEditTestFixLoop(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":        "module example.com/real-eval\n\ngo 1.26\n",
		"value.go":      "package fixture\nfunc Value() int { return 1 }\n",
		"value_test.go": "package fixture\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value() != 3 { t.Fatal(Value()) } }\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	goBinary, err = filepath.Abs(goBinary)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := evaluation.RealAgentEnvironment(goBinary, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	application, _, _ := newTestApp(t, nil)
	userPath := filepath.Join(application.userConfigDir, "mengdie", "config.toml")
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte("[profiles.default]\nprovider = \"openai-compatible\"\nbase_url = \"https://trusted.example/v1\"\nmodel = \"trusted-model\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &appFakeProvider{responses: []*provider.ChatResponse{
		appToolResponse("read", "read_file", map[string]any{"path": "value.go"}),
		appToolResponse("edit-1", "edit_file", map[string]any{"path": "value.go", "old_text": "return 1", "new_text": "return 2", "expected_replacements": 1}),
		appToolResponse("test-1", "shell", map[string]any{"command": "go test ./...", "timeout": "2m"}),
		appToolResponse("edit-2", "edit_file", map[string]any{"path": "value.go", "old_text": "return 2", "new_text": "return 3", "expected_replacements": 1}),
		appToolResponse("test-2", "shell", map[string]any{"command": "go test ./...", "timeout": "2m"}),
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "fixed"}},
	}}
	application.newProvider = func(config.Profile, string) (provider.Provider, error) { return fake, nil }
	result, err := application.RunEvaluation(context.Background(), EvaluationRunOptions{
		Workspace: workspace, StateDir: stateDir, Prompt: "Fix Value", Environment: environment,
		Policy: EvaluationPolicy{AllowedEffects: []tools.Effect{tools.EffectRead, tools.EffectWrite, tools.EffectExecute},
			AllowedChanges: []string{"value.go"}, AllowEdit: true, CommandPrefixes: []string{"go test"},
			MaxToolCalls: 5, MaxCommandCalls: 2},
	})
	if err != nil || result.Status != "completed" || result.ToolCalls != 5 || result.CommandCalls != 2 || result.DeniedTools != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(fake.requests) != 6 || !evaluationRequestContains(fake.requests[3], `"exit_code":"1"`) ||
		!evaluationRequestContains(fake.requests[5], `"exit_code":"0"`) {
		if len(fake.requests) != 6 {
			t.Fatalf("the two test calls did not fail then pass; requests=%d", len(fake.requests))
		}
		t.Fatalf("the two test calls did not fail then pass; first=%s second=%s",
			evaluationShellResult(fake.requests[3]), evaluationShellResult(fake.requests[5]))
	}
	content, err := os.ReadFile(filepath.Join(workspace, "value.go"))
	if err != nil || !strings.Contains(string(content), "return 3") {
		t.Fatalf("final content=%q err=%v", content, err)
	}
}

func evaluationRequestContains(request provider.ChatRequest, fragment string) bool {
	for _, message := range request.Messages {
		if message.Role == provider.RoleTool && strings.Contains(message.Content, fragment) {
			return true
		}
	}
	return false
}

func evaluationShellResult(request provider.ChatRequest) string {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		message := request.Messages[index]
		if message.Role == provider.RoleTool && message.Name == "shell" {
			if len(message.Content) > 1200 {
				return message.Content[:1200]
			}
			return message.Content
		}
	}
	return "missing shell result"
}
