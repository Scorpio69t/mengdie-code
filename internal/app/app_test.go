// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/brand"
	"github.com/Scorpio69t/mengdie-code/internal/events"
)

func TestDoctorJSONDoesNotRevealCredential(t *testing.T) {
	root := t.TempDir()
	writeAppConfig(t, root, `
[profiles.default]
provider = "openai-compatible"
base_url = "https://api.example.com/v1"
api_key_env = "TEST_SECRET_KEY"
model = "test-model"
`)
	application, stdout, _ := newTestApp(t, map[string]string{
		"TEST_SECRET_KEY": "super-secret-value",
	})

	code := application.Run(context.Background(), []string{"doctor", "--cwd", root, "--json"}, false)
	if code != ExitOK {
		t.Fatalf("Run() code = %d, want %d", code, ExitOK)
	}
	if strings.Contains(stdout.String(), "super-secret-value") {
		t.Fatalf("doctor output leaked credential: %s", stdout.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor output: %v", err)
	}
	if !report.CredentialSet || report.APIKeyEnvironment != "TEST_SECRET_KEY" {
		t.Fatalf("doctor report = %+v", report)
	}
}

func TestInteractiveShowsConfiguredModel(t *testing.T) {
	root := t.TempDir()
	writeAppConfig(t, root, `
[profiles.default]
provider = "openai-compatible"
model = "deepseek-chat"
`)
	application, stdout, _ := newTestApp(t, nil)

	code := application.Run(context.Background(), []string{"--cwd", root}, true)
	if code != ExitOK {
		t.Fatalf("Run() code = %d, want %d", code, ExitOK)
	}
	for _, want := range append(strings.Split(brand.Mark, "\n"), "openai-compatible:deepseek-chat", root) {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("interactive output does not contain %q: %s", want, stdout.String())
		}
	}
}

func TestInteractiveOmitsBannerWhenOutputIsRedirected(t *testing.T) {
	root := t.TempDir()
	application, stdout, _ := newTestApp(t, nil)

	code := application.Run(context.Background(), []string{"--cwd", root}, false)
	if code != ExitOK {
		t.Fatalf("Run() code = %d, want %d", code, ExitOK)
	}
	for _, markLine := range strings.Split(brand.Mark, "\n") {
		if strings.Contains(stdout.String(), markLine) {
			t.Fatalf("redirected output unexpectedly contains banner line %q: %s", markLine, stdout.String())
		}
	}
	if !strings.Contains(stdout.String(), "Agent 功能尚未实现") {
		t.Fatalf("redirected output = %q", stdout.String())
	}
}

func TestExecReportsUnavailableWithoutPretendingSuccess(t *testing.T) {
	root := t.TempDir()
	application, stdout, _ := newTestApp(t, nil)

	code := application.Run(context.Background(), []string{"exec", "--cwd", root, "--json", "修复测试"}, false)
	if code != ExitRunError {
		t.Fatalf("Run() code = %d, want %d", code, ExitRunError)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("exec output has %d lines: %q", len(lines), stdout.String())
	}
	wantKinds := []events.Kind{events.KindRunStarted, events.KindRunFailed}
	for i, line := range lines {
		var event events.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode line %d: %v", i, err)
		}
		if event.RunID != "run-test" || event.Seq != uint64(i+1) || event.Kind != wantKinds[i] {
			t.Fatalf("event %d = %+v", i, event)
		}
	}
}

func TestExecHumanOutputUsesEventRenderer(t *testing.T) {
	root := t.TempDir()
	application, stdout, stderr := newTestApp(t, nil)

	code := application.Run(context.Background(), []string{"exec", "--cwd", root, "修复测试"}, true)
	if code != ExitRunError {
		t.Fatalf("Run() code = %d, want %d", code, ExitRunError)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	for _, want := range []string{"开始任务", "任务失败 [runtime_unavailable]", "Agent Runtime 尚未实现"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not contain %q: %s", want, stderr.String())
		}
	}
}

func TestExecCanceledContextUsesStableExitCodeAndDoesNotExposeTask(t *testing.T) {
	root := t.TempDir()
	application, stdout, stderr := newTestApp(t, nil)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	code := application.Run(cancelled, []string{"exec", "--cwd", root, "private prompt"}, false)
	if code != ExitUserCanceled {
		t.Fatalf("Run() code = %d, want %d", code, ExitUserCanceled)
	}
	if strings.Contains(stdout.String(), "private prompt") || strings.Contains(stderr.String(), "private prompt") {
		t.Fatalf("canceled exec exposed task: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestUnknownCommandIsInvalidInput(t *testing.T) {
	application, _, stderr := newTestApp(t, nil)
	if code := application.Run(context.Background(), []string{"unknown"}, false); code != ExitInvalidInput {
		t.Fatalf("Run() code = %d, want %d", code, ExitInvalidInput)
	}
	if !strings.Contains(stderr.String(), "未知命令") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func newTestApp(t *testing.T, environment map[string]string) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	application := New(BuildInfo{Version: "test", Commit: "abc123", Date: "2026-07-30"}, stdout, stderr)
	application.userConfigDir = t.TempDir()
	application.now = func() time.Time {
		return time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	}
	application.newRunID = func() (string, error) { return "run-test", nil }
	application.lookupEnv = func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	}
	return application, stdout, stderr
}

func writeAppConfig(t *testing.T, root, content string) {
	t.Helper()
	path := filepath.Join(root, ".mengdie", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
