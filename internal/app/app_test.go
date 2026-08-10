// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Scorpio69t/mengdie-code/internal/brand"
	"github.com/Scorpio69t/mengdie-code/internal/config"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/session"
	"github.com/Scorpio69t/mengdie-code/internal/tui"
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

	code := application.Run(context.Background(), []string{"doctor", "--cwd", root, "--json", "--offline"}, false)
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
	writeRuntimeConfig(t, root)
	application, stdout, _ := newTestApp(t, nil)
	application.stdin = strings.NewReader("检查项目\n")

	code := application.Run(context.Background(), []string{"--plain", "--cwd", root}, true)
	if code != ExitOK {
		t.Fatalf("Run() code = %d, want %d", code, ExitOK)
	}
	for _, want := range append(strings.Split(brand.Mark, "\n"), "openai-compatible:test-model", root, "fake completed", "任务完成") {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("interactive output does not contain %q: %s", want, stdout.String())
		}
	}
}

func TestInteractiveDefaultsToFullScreenTUI(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	application, stdout, _ := newTestApp(t, nil)
	called := false
	application.runTUI = func(model tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		called = true
		interactive, ok := model.(tui.InteractiveModel)
		if !ok {
			t.Fatalf("default model type = %T", model)
		}
		updated, _ := interactive.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
		interactive = updated.(tui.InteractiveModel)
		content := interactive.View().Content
		for _, want := range []string{"MengDie Code / 梦蝶 Code", root[:20], "openai-compatible:test-model", "等待任务", "Ctrl+S"} {
			if !strings.Contains(content, want) {
				t.Errorf("default TUI missing %q: %s", want, content)
			}
		}
		return interactive, nil
	}

	code := application.Run(context.Background(), []string{"--cwd", root, "--no-color"}, true)
	if code != ExitOK || !called {
		t.Fatalf("Run() code=%d called=%t", code, called)
	}
	if strings.Contains(stdout.String(), "请输入任务（单次有界运行") {
		t.Fatalf("default run used legacy prompt: %s", stdout.String())
	}
}

func TestInteractiveTaskRunnerPublishesCommittedFacts(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	application, _, _ := newTestApp(t, nil)
	loaded, err := application.loadConfig(&commonFlags{cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	broker := tui.NewApprovalBroker()
	defer broker.Close()
	runner := &interactiveTaskRunner{ctx: context.Background(), app: application, loaded: loaded, broker: broker}
	defer runner.Close()
	execution, err := runner.PrepareTask("检查项目")
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := application.factBus.Subscribe(execution.SessionID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if result := execution.Run(); result.ExitCode != ExitOK {
		t.Fatalf("Run() result=%+v", result)
	}

	var kinds []events.Kind
	deadline := time.After(time.Second)
	for {
		select {
		case notification := <-subscription.Notifications():
			kinds = append(kinds, notification.Fact.Kind)
			if notification.Fact.Kind == events.KindRunCompleted {
				if !containsEventKind(kinds, events.KindMessageCompleted) {
					t.Fatalf("facts=%v, missing message.completed", kinds)
				}
				return
			}
		case <-deadline:
			t.Fatalf("facts=%v, missing run.completed", kinds)
		}
	}
}

func TestInteractiveOmitsBannerWhenOutputIsRedirected(t *testing.T) {
	root := t.TempDir()
	application, stdout, stderr := newTestApp(t, nil)
	providerConstructed := false
	application.newProvider = func(config.Profile, string) (provider.Provider, error) {
		providerConstructed = true
		return nil, errors.New("must not be called")
	}

	code := application.Run(context.Background(), []string{"--cwd", root}, false)
	if code != ExitInvalidInput {
		t.Fatalf("Run() code = %d, want %d", code, ExitInvalidInput)
	}
	for _, markLine := range strings.Split(brand.Mark, "\n") {
		if strings.Contains(stdout.String(), markLine) {
			t.Fatalf("redirected output unexpectedly contains banner line %q: %s", markLine, stdout.String())
		}
	}
	if providerConstructed || stdout.Len() != 0 {
		t.Fatalf("redirected run constructed provider=%t stdout=%q", providerConstructed, stdout.String())
	}
	if !strings.Contains(stderr.String(), "请使用 mengdie exec") {
		t.Fatalf("redirected stderr = %q", stderr.String())
	}
}

func TestInteractiveApprovesEditBeforeExecution(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	path := filepath.Join(root, "value.go")
	if err := os.WriteFile(path, []byte("package fixture\nfunc Value() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	application, stdout, _ := newTestApp(t, nil)
	application.stdin = strings.NewReader("修改 value.go\ny\n")
	application.newProvider = func(config.Profile, string) (provider.Provider, error) {
		return &appFakeProvider{responses: []*provider.ChatResponse{
			appToolResponse("edit", "edit_file", map[string]any{
				"path": "value.go", "old_text": "return 1", "new_text": "return 2", "expected_replacements": 1,
			}),
			{Message: provider.Message{Role: provider.RoleAssistant, Content: "修改完成"}},
		}}, nil
	}

	code := application.Run(context.Background(), []string{"--plain", "--cwd", root}, true)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code != ExitOK || !strings.Contains(string(content), "return 2") {
		t.Fatalf("code=%d content=%q", code, content)
	}
	for _, want := range []string{"需要批准", "[y]允许", "修改完成"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("interactive output does not contain %q: %s", want, stdout.String())
		}
	}
}

func TestInteractiveRejectionIsReturnedToModel(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	path := filepath.Join(root, "value.go")
	if err := os.WriteFile(path, []byte("package fixture\nfunc Value() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	application, _, _ := newTestApp(t, nil)
	application.stdin = strings.NewReader("修改 value.go\nn\n")
	fake := &appFakeProvider{responses: []*provider.ChatResponse{
		appToolResponse("edit", "edit_file", map[string]any{
			"path": "value.go", "old_text": "return 1", "new_text": "return 2", "expected_replacements": 1,
		}),
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "已尊重拒绝"}},
	}}
	application.newProvider = func(config.Profile, string) (provider.Provider, error) { return fake, nil }

	code := application.Run(context.Background(), []string{"--plain", "--cwd", root}, true)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code != ExitPolicyDenied || strings.Contains(string(content), "return 2") {
		t.Fatalf("code=%d content=%q", code, content)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("provider requests=%d, want 2", len(fake.requests))
	}
	messages := fake.requests[1].Messages
	if len(messages) == 0 || messages[len(messages)-1].Role != provider.RoleTool || !strings.Contains(messages[len(messages)-1].Content, `"category":"denied"`) {
		t.Fatalf("rejection was not returned as a tool result: %+v", messages)
	}
}

func TestInteractiveApprovesShellBeforeExecution(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	application, stdout, _ := newTestApp(t, nil)
	application.stdin = strings.NewReader("检查 Go 版本\ny\n")
	application.newProvider = func(config.Profile, string) (provider.Provider, error) {
		return &appFakeProvider{responses: []*provider.ChatResponse{
			appToolResponse("shell", "shell", map[string]any{"command": "go version"}),
			{Message: provider.Message{Role: provider.RoleAssistant, Content: "检查完成"}},
		}}, nil
	}

	if code := application.Run(context.Background(), []string{"--plain", "--cwd", root}, true); code != ExitOK {
		t.Fatalf("Run() code = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(stdout.String(), "[y]允许") || !strings.Contains(stdout.String(), "检查完成") {
		t.Fatalf("interactive shell output = %q", stdout.String())
	}
}

func TestSessionTUIRejectsNonInteractiveEntry(t *testing.T) {
	application, _, stderr := newTestApp(t, nil)
	code := application.Run(context.Background(), []string{"session", "tui", "session-test"}, false)
	if code != ExitInvalidInput || !strings.Contains(stderr.String(), "仅支持交互终端") {
		t.Fatalf("code=%d output=%q", code, stderr.String())
	}
}

func TestReadInteractiveTaskBoundsAndCancellation(t *testing.T) {
	tests := []struct {
		name   string
		ctx    context.Context
		input  string
		isErr  error
		result string
	}{
		{name: "trimmed", ctx: context.Background(), input: "  修复测试  \n", result: "修复测试"},
		{name: "empty", ctx: context.Background(), input: "   \n", isErr: errInteractiveTaskEmpty},
		{name: "too large", ctx: context.Background(), input: strings.Repeat("a", maxInteractiveTaskBytes+1) + "\n", isErr: errInteractiveTaskTooLarge},
		{name: "invalid utf8", ctx: context.Background(), input: string([]byte{0xff, '\n'}), isErr: errInteractiveTaskEncoding},
		{name: "cancelled", ctx: canceledContext(), input: "不会读取\n", isErr: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := readInteractiveTask(test.ctx, bufio.NewReader(strings.NewReader(test.input)))
			if !errors.Is(err, test.isErr) || got != test.result {
				t.Fatalf("readInteractiveTask()=(%q, %v), want (%q, %v)", got, err, test.result, test.isErr)
			}
		})
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
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

func TestExecPersistsPrivateTaskOnlyInCommandLedger(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	application, stdout, _ := newTestApp(t, map[string]string{"TEST_API_KEY": "must-not-persist"})

	code := application.Run(context.Background(), []string{"exec", "--cwd", root, "--json", "private task text"}, false)
	if code != ExitOK {
		t.Fatalf("Run() code = %d, want %d", code, ExitOK)
	}
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{DataDir: application.dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestSessionStore(t, store)
	records, err := store.Load(context.Background(), "ses_run-test", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{"run.started", "message.completed", "run.completed"}
	if len(records) != len(wantKinds) {
		t.Fatalf("records=%d: %+v", len(records), records)
	}
	for index, record := range records {
		if record.Kind != wantKinds[index] || record.SessionSeq != uint64(index+1) || record.CommandID != "cmd_run-test" {
			t.Fatalf("record %d=%+v", index, record)
		}
		if bytes.Contains(record.Payload, []byte("private task text")) {
			t.Fatalf("public event contains private task: %s", record.Payload)
		}
	}
	if strings.Contains(stdout.String(), "private task text") {
		t.Fatalf("JSON output exposed task: %q", stdout.String())
	}
	databaseBytes, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	command, err := store.LookupCommand(context.Background(), "cmd_run-test")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(command.Payload, []byte("private task text")) {
		t.Fatalf("command payload=%s", command.Payload)
	}
	for _, forbidden := range []string{"must-not-persist"} {
		if bytes.Contains(databaseBytes, []byte(forbidden)) {
			t.Fatalf("database contains forbidden value %q", forbidden)
		}
	}
}

func TestExecStoreFirstFailureSemantics(t *testing.T) {
	t.Run("renderer failure leaves durable fact", func(t *testing.T) {
		root := t.TempDir()
		writeRuntimeConfig(t, root)
		application, _, _ := newTestApp(t, nil)
		application.stdout = appFailingWriter{err: errors.New("renderer failed")}
		if code := application.Run(context.Background(), []string{"exec", "--cwd", root, "--json", "task"}, false); code != ExitRunError {
			t.Fatalf("Run() code=%d", code)
		}
		store, err := session.OpenSQLite(context.Background(), session.OpenOptions{DataDir: application.dataDir})
		if err != nil {
			t.Fatal(err)
		}
		defer closeTestSessionStore(t, store)
		records, err := store.Load(context.Background(), "ses_run-test", 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) == 0 || records[0].Kind != "run.started" {
			t.Fatalf("records=%+v", records)
		}
	})
	t.Run("store failure is not rendered", func(t *testing.T) {
		root := t.TempDir()
		writeRuntimeConfig(t, root)
		application, stdout, stderr := newTestApp(t, nil)
		application.dataDir = filepath.Join(root, ".mengdie-data")
		if code := application.Run(context.Background(), []string{"exec", "--cwd", root, "--json", "task"}, false); code != ExitRunError {
			t.Fatalf("Run() code=%d", code)
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "会话存储错误") {
			t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})
}

func TestExecCommandIDReplaysTerminalFactsWithoutProvider(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	application, stdout, stderr := newTestApp(t, nil)
	providerCreations := 0
	application.newProvider = func(config.Profile, string) (provider.Provider, error) {
		providerCreations++
		return &appFakeProvider{responses: []*provider.ChatResponse{{Message: provider.Message{Role: provider.RoleAssistant, Content: "once"}}}}, nil
	}
	ids := []string{"run-first", "run-retry", "run-conflict"}
	application.newRunID = func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	args := []string{"exec", "--cwd", root, "--json", "--command-id", "stable-1", "private task"}
	if code := application.Run(context.Background(), args, false); code != ExitOK {
		t.Fatalf("first code=%d stderr=%s", code, stderr.String())
	}
	firstOutput := stdout.String()
	stdout.Reset()
	if code := application.Run(context.Background(), args, false); code != ExitOK {
		t.Fatalf("retry code=%d stderr=%s", code, stderr.String())
	}
	if providerCreations != 1 {
		t.Fatalf("provider creations=%d", providerCreations)
	}
	if strings.Contains(stdout.String(), "private task") {
		t.Fatalf("replay leaked private task: %s", stdout.String())
	}
	if strings.Count(strings.TrimSpace(stdout.String()), "\n")+1 != 3 {
		t.Fatalf("replay output=%q first=%q", stdout.String(), firstOutput)
	}
	stdout.Reset()
	conflict := []string{"exec", "--cwd", root, "--json", "--command-id", "stable-1", "different task"}
	if code := application.Run(context.Background(), conflict, false); code != ExitRunError {
		t.Fatalf("conflict code=%d", code)
	}
	if providerCreations != 1 {
		t.Fatalf("provider called after conflict: %d", providerCreations)
	}
}

func TestExecCommandIDFailsClosedForRunningCommand(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	application, stdout, stderr := newTestApp(t, nil)
	payload, err := session.TaskCommandPayload("private task")
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{DataDir: application.dataDir})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.BeginCommandRun(context.Background(), session.CommandRunMetadata{
		SessionID: "session-running", CommandID: "stable-running", CommandKind: session.CommandKindExec,
		CommandPayload: payload, RunID: "run-running", ProjectRoot: filepath.Clean(root),
		Provider: "openai-compatible", Model: "test-model", StartedAt: application.now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := events.New("run-running", 1, application.now(), events.KindRunStarted, events.RunStarted{Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	sink, err := session.NewCommandEventSink("session-running", "stable-running", 0, store, &events.MemorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Emit(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	closeTestSessionStore(t, store)
	providerCreations := 0
	application.newProvider = func(config.Profile, string) (provider.Provider, error) {
		providerCreations++
		return &appFakeProvider{}, nil
	}
	code := application.Run(context.Background(), []string{"exec", "--cwd", root, "--json", "--command-id", "stable-running", "private task"}, false)
	if code != ExitRunError || providerCreations != 0 {
		t.Fatalf("code=%d provider creations=%d", code, providerCreations)
	}
	if !strings.Contains(stderr.String(), "不会自动续跑") || strings.Contains(stdout.String(), "private task") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestExecRejectsUnsafeCommandID(t *testing.T) {
	application, _, stderr := newTestApp(t, nil)
	code := application.Run(context.Background(), []string{"exec", "--command-id", "unsafe id", "task"}, false)
	if code != ExitInvalidInput || !strings.Contains(stderr.String(), "--command-id") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestSessionCommandsExposeOnlyPublicProjectionAndRequireDeleteConfirmation(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	application, stdout, stderr := newTestApp(t, nil)
	if code := application.Run(context.Background(), []string{"exec", "--cwd", root, "--json", "private task"}, false); code != ExitOK {
		t.Fatalf("exec code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	if code := application.Run(context.Background(), []string{"session", "list", "--cwd", root, "--json"}, false); code != ExitOK {
		t.Fatalf("list code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ses_run-test") || strings.Contains(stdout.String(), "private task") {
		t.Fatalf("list=%s", stdout.String())
	}
	stdout.Reset()
	if code := application.Run(context.Background(), []string{"session", "show", "--cwd", root, "--json", "ses_run-test"}, false); code != ExitOK {
		t.Fatalf("show code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "fake completed") || strings.Contains(stdout.String(), "private task") {
		t.Fatalf("show=%s", stdout.String())
	}
	stdout.Reset()
	if code := application.Run(context.Background(), []string{"session", "delete", "--cwd", root, "ses_run-test"}, false); code != ExitInvalidInput {
		t.Fatalf("delete without yes code=%d", code)
	}
	if code := application.Run(context.Background(), []string{"session", "delete", "--cwd", root, "--yes", "--json", "ses_run-test"}, false); code != ExitOK {
		t.Fatalf("delete code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"deleted":true`) {
		t.Fatalf("delete output=%s", stdout.String())
	}
}

func TestSessionResumeRestoresHistoryInSameSessionAndReplaysIdempotently(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	application, stdout, stderr := newTestApp(t, nil)
	ids := []string{"run-first", "run-resume", "run-replay"}
	application.newRunID = func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	firstProvider := &appFakeProvider{responses: []*provider.ChatResponse{{
		Message: provider.Message{Role: provider.RoleAssistant, Content: "第一轮完成"},
	}}}
	resumeProvider := &appFakeProvider{responses: []*provider.ChatResponse{{
		Message: provider.Message{Role: provider.RoleAssistant, Content: "恢复完成"},
	}}}
	providerCreations := 0
	application.newProvider = func(config.Profile, string) (provider.Provider, error) {
		providerCreations++
		if providerCreations == 1 {
			return firstProvider, nil
		}
		return resumeProvider, nil
	}
	if code := application.Run(context.Background(), []string{"exec", "--cwd", root, "--json", "初始私有任务"}, false); code != ExitOK {
		t.Fatalf("first code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	resumeArgs := []string{
		"session", "resume", "--cwd", root, "--json", "--command-id", "resume-stable",
		"--message", "继续检查当前仓库", "ses_run-first",
	}
	if code := application.Run(context.Background(), resumeArgs, false); code != ExitOK {
		t.Fatalf("resume code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if len(resumeProvider.requests) != 1 {
		t.Fatalf("resume provider requests=%d", len(resumeProvider.requests))
	}
	contents := make([]string, 0, len(resumeProvider.requests[0].Messages))
	for _, message := range resumeProvider.requests[0].Messages {
		contents = append(contents, message.Content)
	}
	joined := strings.Join(contents, "\n")
	for _, want := range []string{"初始私有任务", "第一轮完成", "继续检查当前仓库"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("restored request missing %q: %+v", want, resumeProvider.requests[0].Messages)
		}
	}
	stdout.Reset()
	if code := application.Run(context.Background(), resumeArgs, false); code != ExitOK {
		t.Fatalf("resume replay code=%d stderr=%s", code, stderr.String())
	}
	if providerCreations != 2 || len(resumeProvider.requests) != 1 {
		t.Fatalf("idempotent replay called provider: creations=%d requests=%d", providerCreations, len(resumeProvider.requests))
	}
	if strings.Contains(stdout.String(), "初始私有任务") || strings.Contains(stdout.String(), "继续检查当前仓库") {
		t.Fatalf("replay leaked private context: %s", stdout.String())
	}
	stdout.Reset()
	if code := application.Run(context.Background(), []string{"session", "show", "--cwd", root, "--json", "ses_run-first"}, false); code != ExitOK {
		t.Fatalf("show resumed code=%d stderr=%s", code, stderr.String())
	}
	if strings.Count(stdout.String(), `"status":"completed"`) < 3 || !strings.Contains(stdout.String(), "恢复完成") {
		t.Fatalf("resumed view=%s", stdout.String())
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
			providerCreations := 0
			application.newProvider = func(config.Profile, string) (provider.Provider, error) {
				providerCreations++
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
			if !allow {
				if retryCode := application.Run(context.Background(), args, false); retryCode != ExitPolicyDenied {
					t.Fatalf("retry code=%d want=%d", retryCode, ExitPolicyDenied)
				}
				if providerCreations != 1 {
					t.Fatalf("provider creations after rejected replay=%d", providerCreations)
				}
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
	application.dataDir = t.TempDir()
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

[approval]
allow_commands = []
`)
}

type appFakeProvider struct {
	responses []*provider.ChatResponse
	requests  []provider.ChatRequest
	index     int
}

func (*appFakeProvider) ID() string { return "fake" }

func (*appFakeProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{ToolCalling: true, MaxContextTokens: 64_000}, nil
}

func (fake *appFakeProvider) Stream(ctx context.Context, request provider.ChatRequest, sink provider.StreamSink) (*provider.ChatResponse, error) {
	fake.requests = append(fake.requests, request)
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

func closeTestSessionStore(t *testing.T, store *session.SQLiteStore) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Errorf("close session store: %v", err)
	}
}

func containsEventKind(kinds []events.Kind, target events.Kind) bool {
	for _, kind := range kinds {
		if kind == target {
			return true
		}
	}
	return false
}
