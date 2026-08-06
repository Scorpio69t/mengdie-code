// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/config"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

func TestDoctorOnlineProbeObservesToolCalling(t *testing.T) {
	root := t.TempDir()
	writeAppConfig(t, root, `
[profiles.default]
provider = "openai-compatible"
base_url = "https://api.example.com/v1"
api_key_env = "TEST_SECRET_KEY"
model = "test-model"
`)
	application, stdout, _ := newTestApp(t, map[string]string{"TEST_SECRET_KEY": "secret"})
	fake := &appFakeProvider{responses: []*provider.ChatResponse{
		appToolResponse("doctor-call", doctorProbeTool, map[string]any{"value": doctorProbeToken}),
	}}
	application.newProvider = func(config.Profile, string) (provider.Provider, error) {
		return fake, nil
	}

	code := application.Run(context.Background(), []string{"doctor", "--cwd", root, "--json"}, false)
	if code != ExitOK {
		t.Fatalf("Run() code=%d output=%s", code, stdout.String())
	}
	report := decodeDoctorReport(t, stdout.String())
	if report.Status == "error" || report.ProviderProbe.Status != "passed" || !report.ProviderProbe.ToolCalling {
		t.Fatalf("report=%+v", report)
	}
	if report.Capabilities == nil || !report.Capabilities.ToolCalling {
		t.Fatalf("capabilities=%+v", report.Capabilities)
	}
	if len(fake.requests) != 1 || fake.requests[0].ToolChoice != provider.ToolChoiceRequired ||
		len(fake.requests[0].Tools) != 1 || fake.requests[0].Tools[0].Function.Name != doctorProbeTool {
		t.Fatalf("probe request=%+v", fake.requests)
	}
	requestJSON, err := json.Marshal(fake.requests[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{root, "secret", "TEST_SECRET_KEY"} {
		if strings.Contains(string(requestJSON), forbidden) {
			t.Fatalf("probe request leaked %q: %s", forbidden, requestJSON)
		}
	}
}

func TestDoctorOfflineNeverConstructsProvider(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	application, stdout, _ := newTestApp(t, nil)
	application.newProvider = func(config.Profile, string) (provider.Provider, error) {
		t.Fatal("offline doctor constructed a provider")
		return nil, nil
	}

	code := application.Run(context.Background(), []string{"doctor", "--cwd", root, "--offline", "--json"}, false)
	if code != ExitOK {
		t.Fatalf("Run() code=%d output=%s", code, stdout.String())
	}
	report := decodeDoctorReport(t, stdout.String())
	if !report.Offline || report.ProviderProbe.Attempted || report.ProviderProbe.Status != "skipped" {
		t.Fatalf("provider probe=%+v", report.ProviderProbe)
	}
}

func TestDoctorClassifiesHTTPAuthenticationWithoutLeakingCredential(t *testing.T) {
	const secret = "doctor-live-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("provider request did not carry configured bearer credential")
		}
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	root := t.TempDir()
	writeAppConfig(t, root, `
[profiles.default]
provider = "openai-compatible"
base_url = "`+server.URL+`"
api_key_env = "TEST_SECRET_KEY"
model = "test-model"
request_timeout = "5s"
`)
	application, stdout, _ := newTestApp(t, map[string]string{"TEST_SECRET_KEY": secret})
	application.newProvider = defaultProviderFactory

	code := application.Run(context.Background(), []string{"doctor", "--cwd", root, "--json", "--probe-timeout", "5s"}, false)
	if code != ExitProviderError {
		t.Fatalf("Run() code=%d output=%s", code, stdout.String())
	}
	if strings.Contains(stdout.String(), secret) {
		t.Fatalf("doctor leaked credential: %s", stdout.String())
	}
	report := decodeDoctorReport(t, stdout.String())
	if report.ProviderProbe.Category != string(provider.ErrorAuthentication) || report.ProviderProbe.Code != "http_status_401" {
		t.Fatalf("provider probe=%+v", report.ProviderProbe)
	}
}

func TestDoctorReportsAgentsChainWithRedactedPaths(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "nested")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(root, "AGENTS.md"):    "root",
		filepath.Join(workDir, "AGENTS.md"): "nested",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	application, stdout, _ := newTestApp(t, nil)
	userAgents := filepath.Join(application.userConfigDir, "mengdie", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(userAgents), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userAgents, []byte("user"), 0o600); err != nil {
		t.Fatal(err)
	}

	code := application.Run(context.Background(), []string{"doctor", "--cwd", workDir, "--offline", "--json"}, false)
	if code != ExitOK {
		t.Fatalf("Run() code=%d output=%s", code, stdout.String())
	}
	report := decodeDoctorReport(t, stdout.String())
	want := []string{
		filepath.Join("$USER_CONFIG", "AGENTS.md"),
		filepath.Join("$PROJECT_ROOT", "AGENTS.md"),
		filepath.Join("$PROJECT_ROOT", "nested", "AGENTS.md"),
	}
	if strings.Join(report.Agents, "|") != strings.Join(want, "|") {
		t.Fatalf("agents=%q want=%q", report.Agents, want)
	}
	if strings.Contains(stdout.String(), root) || strings.Contains(stdout.String(), application.userConfigDir) {
		t.Fatalf("doctor output leaked local paths: %s", stdout.String())
	}
}

func TestDoctorRejectsUnboundedProbeTimeout(t *testing.T) {
	application, _, _ := newTestApp(t, nil)
	for _, timeout := range []string{"500ms", "2m"} {
		if code := application.Run(context.Background(), []string{"doctor", "--probe-timeout", timeout}, false); code != ExitInvalidInput {
			t.Fatalf("timeout=%s code=%d", timeout, code)
		}
	}
}

func TestDoctorGitEnvironmentDropsCredentials(t *testing.T) {
	environment := doctorGitEnvironment([]string{
		"PATH=/tools",
		"HOME=/home/test",
		"DEEPSEEK_API_KEY=secret",
		"GITHUB_TOKEN=secret",
		"Path=/ignored-duplicate",
	})
	joined := strings.Join(environment, "\n")
	for _, want := range []string{"PATH=/tools", "HOME=/home/test", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment does not contain %q: %q", want, environment)
		}
	}
	for _, forbidden := range []string{"DEEPSEEK_API_KEY", "GITHUB_TOKEN", "/ignored-duplicate"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("environment contains %q: %q", forbidden, environment)
		}
	}
}

func TestDoctorDistinguishesRedirectedInputFromTTYOutput(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "doctor-input")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close() })
	if doctorInteractiveInput(input, true) {
		t.Fatal("regular input file was reported as interactive")
	}
	if !doctorInteractiveInput(strings.NewReader("yes"), true) {
		t.Fatal("injected test input was not reported as interactive")
	}
	if doctorInteractiveInput(strings.NewReader("yes"), false) {
		t.Fatal("non-TTY output unexpectedly enabled interactive input")
	}
}

func decodeDoctorReport(t *testing.T, raw string) doctorReport {
	t.Helper()
	var report doctorReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("decode doctor report: %v\n%s", err, raw)
	}
	return report
}
