//go:build liveprovider

// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/evaluation"
	"github.com/Scorpio69t/mengdie-code/internal/events"
)

func TestLiveProviderCompletesReadOnlyToolTask(t *testing.T) {
	baseURL, apiKey, model := configureLiveProviderEnvironment(t)

	root := t.TempDir()
	const fixtureName = "MENGDIE_SMOKE.txt"
	const fixtureContent = "mengdie-live-readonly-ok\n"
	fixturePath := filepath.Join(root, fixtureName)
	if err := os.WriteFile(fixturePath, []byte(fixtureContent), 0o600); err != nil {
		t.Fatal(err)
	}
	writeLiveProviderConfig(t, root, baseURL, model)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	application := New(BuildInfo{Version: "live-smoke", Commit: "manual"}, &stdout, &stderr)
	application.userConfigDir = t.TempDir()
	application.userHomeDir = t.TempDir()
	application.dataDir = t.TempDir()
	code := application.Run(context.Background(), []string{
		"exec", "--cwd", root, "--json", "--max-turns", "8",
		"必须使用 read_file 读取 MENGDIE_SMOKE.txt，然后只回答文件中的标记；禁止修改文件或执行命令。",
	}, false)
	if strings.Contains(stdout.String(), apiKey) || strings.Contains(stderr.String(), apiKey) {
		t.Fatal("live smoke output leaked the Provider credential")
	}
	if code != ExitOK {
		t.Fatalf("live smoke exit=%d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	content, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != fixtureContent {
		t.Fatalf("live smoke modified fixture: %q", content)
	}

	sawRead := false
	sawCompleted := false
	for index, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var event events.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event %d: %v", index, err)
		}
		switch event.Kind {
		case events.KindToolProposed:
			var proposed events.ToolProposed
			if err := json.Unmarshal(event.Payload, &proposed); err != nil {
				t.Fatal(err)
			}
			sawRead = sawRead || proposed.Tool == "read_file"
		case events.KindRunCompleted:
			sawCompleted = true
		}
	}
	if !sawRead || !sawCompleted {
		t.Fatalf("live smoke did not prove the read-only loop: read=%t completed=%t", sawRead, sawCompleted)
	}
}

func TestLiveProviderCompletesCodingTaskSuite(t *testing.T) {
	baseURL, apiKey, model := configureLiveProviderEnvironment(t)
	t.Setenv("GOWORK", "off")

	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(repositoryRoot, "evals", "coding", "smoke.json")
	manifest, err := evaluation.LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tasks) != 5 {
		t.Fatalf("M1 coding acceptance requires exactly 5 tasks, got %d", len(manifest.Tasks))
	}
	fixtureRoot := filepath.Join(repositoryRoot, "evals", "fixtures")

	for _, task := range manifest.Tasks {
		t.Run(task.ID, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "workspace")
			source := filepath.Join(fixtureRoot, filepath.FromSlash(task.Fixture))
			if err := os.CopyFS(workspace, os.DirFS(source)); err != nil {
				t.Fatalf("copy fixture: %v", err)
			}
			writeLiveProviderConfig(t, workspace, baseURL, model)
			before, err := snapshotWorkspace(workspace)
			if err != nil {
				t.Fatal(err)
			}
			if len(task.Acceptance.AllowedChanges) == 0 {
				t.Fatal("live coding task requires acceptance.allowed_changes")
			}

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			application := New(BuildInfo{Version: "live-coding", Commit: "manual"}, &stdout, &stderr)
			application.userConfigDir = t.TempDir()
			application.userHomeDir = t.TempDir()
			application.dataDir = t.TempDir()
			prompt := task.Prompt + "\n必须先用 read_file 阅读相关源码和测试；只修改完成任务所需的项目文件；必须执行 go test ./...，失败时根据结果继续修正，测试通过后才能结束。禁止新增依赖、访问网络或执行 go test 之外的命令。"
			code := application.Run(context.Background(), []string{
				"exec", "--cwd", workspace, "--json", "--max-turns", "24",
				"--allow-edit", "--allow-command", "go,test", prompt,
			}, false)
			combinedOutput := stdout.String() + stderr.String()
			if strings.Contains(combinedOutput, apiKey) {
				t.Fatal("live coding output leaked the Provider credential")
			}
			if code != ExitOK {
				t.Fatalf("live coding exit=%d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
			}

			evidence := inspectCodingEvidence(t, stdout.String())
			if !evidence.read || !evidence.write || !evidence.shell || !evidence.completed || evidence.approvalNeeded {
				t.Fatalf("incomplete coding evidence: read=%t write=%t shell=%t completed=%t approval_needed=%t",
					evidence.read, evidence.write, evidence.shell, evidence.completed, evidence.approvalNeeded)
			}
			after, err := snapshotWorkspace(workspace)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateWorkspaceChanges(before, after, task.Acceptance.AllowedChanges); err != nil {
				t.Fatal(err)
			}
			runTaskVerifier(t, workspace, task)
			t.Logf("M1 coding task passed with read/edit/test evidence: %s", task.ID)
		})
	}
}

type codingEvidence struct {
	read           bool
	write          bool
	shell          bool
	completed      bool
	approvalNeeded bool
}

func inspectCodingEvidence(t *testing.T, output string) codingEvidence {
	t.Helper()
	evidence := codingEvidence{}
	for index, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event events.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode coding event %d: %v", index, err)
		}
		switch event.Kind {
		case events.KindToolCompleted:
			var completed events.ToolCompleted
			if err := json.Unmarshal(event.Payload, &completed); err != nil {
				t.Fatal(err)
			}
			if !completed.Success {
				continue
			}
			switch completed.Tool {
			case "read_file":
				evidence.read = true
			case "edit_file", "write_file":
				evidence.write = true
			case "shell":
				evidence.shell = true
			}
		case events.KindApprovalNeeded:
			evidence.approvalNeeded = true
		case events.KindRunCompleted:
			evidence.completed = true
		}
	}
	return evidence
}

func runTaskVerifier(t *testing.T, workspace string, task evaluation.Task) {
	t.Helper()
	timeout := 30 * time.Second
	if strings.TrimSpace(task.Verify.Timeout) != "" {
		parsed, err := time.ParseDuration(task.Verify.Timeout)
		if err != nil {
			t.Fatal(err)
		}
		timeout = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, task.Verify.Command[0], task.Verify.Command[1:]...)
	command.Dir = workspace
	command.Env = environmentWithout(os.Environ(), "MENGDIE_LIVE_API_KEY")
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("verification timed out after %s", timeout)
	}
	if err != nil {
		t.Fatalf("verification failed: %v\n%s", err, output)
	}
}

func environmentWithout(environment []string, names ...string) []string {
	filtered := make([]string, 0, len(environment))
	for _, item := range environment {
		name, _, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		remove := false
		for _, denied := range names {
			if strings.EqualFold(name, denied) {
				remove = true
				break
			}
		}
		if !remove {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

type workspaceSnapshot map[string][sha256.Size]byte

func snapshotWorkspace(root string) (workspaceSnapshot, error) {
	snapshot := make(workspaceSnapshot)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace entry %q is a symbolic link", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("workspace entry %q is not a regular file", path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(relative)] = sha256.Sum256(content)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot workspace: %w", err)
	}
	return snapshot, nil
}

func validateWorkspaceChanges(before, after workspaceSnapshot, allowedChanges []string) error {
	allowed := make(map[string]struct{}, len(allowedChanges))
	for _, path := range allowedChanges {
		allowed[path] = struct{}{}
	}
	changed := 0
	for path, beforeDigest := range before {
		afterDigest, exists := after[path]
		if !exists {
			return fmt.Errorf("evaluated Agent deleted %q", path)
		}
		if beforeDigest == afterDigest {
			continue
		}
		if _, ok := allowed[path]; !ok {
			return fmt.Errorf("evaluated Agent changed non-allowlisted file %q", path)
		}
		changed++
	}
	for path := range after {
		if _, exists := before[path]; exists {
			continue
		}
		if _, ok := allowed[path]; !ok {
			return fmt.Errorf("evaluated Agent created non-allowlisted file %q", path)
		}
		changed++
	}
	if changed == 0 {
		return fmt.Errorf("evaluated Agent did not change an allowlisted file")
	}
	return nil
}

func TestInspectCodingEvidenceUsesSuccessfulToolEvents(t *testing.T) {
	testCases := []struct {
		kind    events.Kind
		payload any
	}{
		{events.KindToolCompleted, events.ToolCompleted{Tool: "read_file", Success: true}},
		{events.KindToolCompleted, events.ToolCompleted{Tool: "edit_file", Success: true}},
		{events.KindToolCompleted, events.ToolCompleted{Tool: "shell", Success: false}},
		{events.KindToolCompleted, events.ToolCompleted{Tool: "shell", Success: true}},
		{events.KindApprovalNeeded, events.ApprovalNeeded{CallID: "call-1"}},
		{events.KindRunCompleted, events.RunCompleted{}},
	}
	var output strings.Builder
	for index, testCase := range testCases {
		event, err := events.New("run-live-test", uint64(index+1), time.Unix(int64(index), 0), testCase.kind, testCase.payload)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}

	evidence := inspectCodingEvidence(t, output.String())
	if !evidence.read || !evidence.write || !evidence.shell || !evidence.completed || !evidence.approvalNeeded {
		t.Fatalf("unexpected evidence: %+v", evidence)
	}
}

func TestEnvironmentWithoutRemovesCredentialCaseInsensitively(t *testing.T) {
	filtered := environmentWithout([]string{
		"PATH=/bin",
		"mengdie_live_api_key=secret",
		"GOWORK=off",
	}, "MENGDIE_LIVE_API_KEY")
	joined := strings.Join(filtered, "\n")
	if strings.Contains(strings.ToLower(joined), "api_key") {
		t.Fatalf("credential remained in environment: %q", joined)
	}
	if !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "GOWORK=off") {
		t.Fatalf("non-secret environment was removed: %q", joined)
	}
}

func TestValidateWorkspaceChangesRejectsUnrelatedDiff(t *testing.T) {
	before := workspaceSnapshot{
		"value.go":      sha256.Sum256([]byte("broken")),
		"value_test.go": sha256.Sum256([]byte("test")),
	}
	after := workspaceSnapshot{
		"value.go":      sha256.Sum256([]byte("fixed")),
		"value_test.go": sha256.Sum256([]byte("weakened test")),
	}
	if err := validateWorkspaceChanges(before, after, []string{"value.go"}); err == nil {
		t.Fatal("validateWorkspaceChanges() error = nil, want unrelated diff error")
	}
	after["value_test.go"] = before["value_test.go"]
	if err := validateWorkspaceChanges(before, after, []string{"value.go"}); err != nil {
		t.Fatalf("validateWorkspaceChanges() error = %v, want allowed change", err)
	}
}

func configureLiveProviderEnvironment(t *testing.T) (baseURL, apiKey, model string) {
	t.Helper()
	if os.Getenv("MENGDIE_LIVE_SMOKE") != "1" {
		t.Skip("set MENGDIE_LIVE_SMOKE=1 and live Provider variables to run")
	}
	baseURL = requiredLiveEnvironment(t, "MENGDIE_LIVE_BASE_URL")
	apiKey = requiredLiveEnvironment(t, "MENGDIE_LIVE_API_KEY")
	model = requiredLiveEnvironment(t, "MENGDIE_LIVE_MODEL")
	t.Setenv("MENGDIE_PROFILE", "default")
	t.Setenv("MENGDIE_BASE_URL", baseURL)
	t.Setenv("MENGDIE_MODEL", model)
	t.Setenv("MENGDIE_API_KEY_ENV", "MENGDIE_LIVE_API_KEY")
	return baseURL, apiKey, model
}

func writeLiveProviderConfig(t *testing.T, root, baseURL, model string) {
	t.Helper()
	configPath := filepath.Join(root, ".mengdie", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	configContent := fmt.Sprintf(`
[profiles.default]
provider = "openai-compatible"
base_url = %s
api_key_env = "MENGDIE_LIVE_API_KEY"
model = %s
request_timeout = "120s"
max_context_tokens = 64000
`, strconv.Quote(baseURL), strconv.Quote(model))
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requiredLiveEnvironment(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required when MENGDIE_LIVE_SMOKE=1", name)
	}
	return value
}
