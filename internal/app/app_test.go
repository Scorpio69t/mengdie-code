// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/brand"
	"github.com/Scorpio69t/mengdie-code/internal/config"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
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

func TestExecRunsAgentAndEmitsCompletedEvents(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	application, stdout, _ := newTestApp(t, nil)

	code := application.Run(context.Background(), []string{"exec", "--cwd", root, "--json", "修复测试"}, false)
	if code != ExitOK {
		t.Fatalf("Run() code = %d, want %d", code, ExitOK)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("exec output has %d lines: %q", len(lines), stdout.String())
	}
	wantKinds := []events.Kind{events.KindRunStarted, events.KindMessageDelta, events.KindMessageCompleted, events.KindRunCompleted}
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
	writeRuntimeConfig(t, root)
	application, stdout, stderr := newTestApp(t, nil)

	code := application.Run(context.Background(), []string{"exec", "--cwd", root, "修复测试"}, true)
	if code != ExitOK {
		t.Fatalf("Run() code = %d, want %d", code, ExitOK)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	for _, want := range []string{"开始任务", "fake completed", "任务完成"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not contain %q: %s", want, stderr.String())
		}
	}
}

func TestExecRequiresExplicitEditAuthorization(t *testing.T) {
	for name, allow := range map[string]bool{"denied": false, "allowed": true} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRuntimeConfig(t, root)
			path := filepath.Join(root, "value.go")
			if err := os.WriteFile(path, []byte("package fixture\nfunc Value() int { return 1 }\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			application, _, _ := newTestApp(t, nil)
			application.newProvider = func(config.Profile, string) (provider.Provider, error) {
				return &appFakeProvider{responses: []*provider.ChatResponse{
					appToolResponse("edit", "edit_file", map[string]any{
						"path": "value.go", "old_text": "return 1", "new_text": "return 2", "expected_replacements": 1,
					}),
					{Message: provider.Message{Role: provider.RoleAssistant, Content: "处理完成"}},
				}}, nil
			}
			args := []string{"exec", "--cwd", root}
			if allow {
				args = append(args, "--allow-edit")
			}
			args = append(args, "修改")
			code := application.Run(context.Background(), args, false)
			wantCode := ExitPolicyDenied
			wantContent := "return 1"
			if allow {
				wantCode = ExitOK
				wantContent = "return 2"
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if code != wantCode || !strings.Contains(string(content), wantContent) {
				t.Fatalf("code=%d want=%d content=%q", code, wantCode, content)
			}
		})
	}
}

func TestExecCanceledContextUsesStableExitCodeAndDoesNotExposeTask(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
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

func TestCommandPrefixFlagTreatsCommaAsTokenSeparator(t *testing.T) {
	var flag commandPrefixFlag
	if err := flag.Set("go,test"); err != nil {
		t.Fatal(err)
	}
	if got := flag.Values(); len(got) != 1 || got[0] != "go test" {
		t.Fatalf("Values()=%v", got)
	}
	if err := flag.Set("git status"); err != nil {
		t.Fatal(err)
	}
	if got := flag.Values(); len(got) != 2 || got[1] != "git status" {
		t.Fatalf("Values()=%v", got)
	}
}

func TestOutputFailuresUseRunErrorExitCode(t *testing.T) {
	want := errors.New("writer failed")

	t.Run("version output", func(t *testing.T) {
		application, _, _ := newTestApp(t, nil)
		application.stdout = appFailingWriter{err: want}
		if code := application.Run(context.Background(), []string{"version"}, false); code != ExitRunError {
			t.Fatalf("Run() code = %d, want %d", code, ExitRunError)
		}
	})

	t.Run("diagnostic output", func(t *testing.T) {
		application, _, _ := newTestApp(t, nil)
		application.stderr = appFailingWriter{err: want}
		if code := application.Run(context.Background(), []string{"unknown"}, false); code != ExitRunError {
			t.Fatalf("Run() code = %d, want %d", code, ExitRunError)
		}
	})

	t.Run("doctor output", func(t *testing.T) {
		application, _, _ := newTestApp(t, nil)
		application.stdout = appFailingWriter{err: want}
		if code := application.Run(context.Background(), []string{"doctor", "--cwd", t.TempDir()}, false); code != ExitRunError {
			t.Fatalf("Run() code = %d, want %d", code, ExitRunError)
		}
	})
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
	application.newProvider = func(config.Profile, string) (provider.Provider, error) {
		return &appFakeProvider{responses: []*provider.ChatResponse{{
			Message: provider.Message{Role: provider.RoleAssistant, Content: "fake completed"},
		}}}, nil
	}
	application.lookupEnv = func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	}
	return application, stdout, stderr
}

func writeRuntimeConfig(t *testing.T, root string) {
	t.Helper()
	writeAppConfig(t, root, `
[profiles.default]
provider = "openai-compatible"
base_url = "https://api.example.com/v1"
model = "test-model"
`)
}

type appFakeProvider struct {
	responses []*provider.ChatResponse
	index     int
}

func (*appFakeProvider) ID() string { return "fake" }

func (*appFakeProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{ToolCalling: true, MaxContextTokens: 64_000}, nil
}

func (fake *appFakeProvider) Stream(ctx context.Context, _ provider.ChatRequest, sink provider.StreamSink) (*provider.ChatResponse, error) {
	if fake.index >= len(fake.responses) {
		return nil, errors.New("fake responses exhausted")
	}
	response := fake.responses[fake.index]
	fake.index++
	if response.Message.Content != "" {
		if err := sink.OnEvent(ctx, provider.StreamEvent{Kind: provider.StreamTextDelta, Text: response.Message.Content}); err != nil {
			return nil, err
		}
	}
	return response, nil
}

func appToolResponse(id, name string, arguments any) *provider.ChatResponse {
	raw, _ := json.Marshal(arguments)
	return &provider.ChatResponse{Message: provider.Message{
		Role:      provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{ID: id, Type: "function", Name: name, Arguments: raw}},
	}}
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

type appFailingWriter struct {
	err error
}

func (writer appFailingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
