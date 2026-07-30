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

	"github.com/Scorpio69t/mengdie-code/internal/brand"
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
	for _, want := range []string{brand.Mark, "openai-compatible:deepseek-chat", root} {
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
	if strings.Contains(stdout.String(), brand.Mark) {
		t.Fatalf("redirected output unexpectedly contains banner: %s", stdout.String())
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
	if !strings.Contains(stdout.String(), `"kind":"run.unavailable"`) {
		t.Fatalf("exec output = %q", stdout.String())
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

