// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestShellPrepareBindsExecutionContractWithoutEnvironmentValues(t *testing.T) {
	env := newToolTestEnv(t)
	baseEnvironment := append(os.Environ(),
		"MENGDIE_TEST_SAFE=visible-value",
		"MENGDIE_TEST_SECRET_TOKEN=secret-value",
	)
	prepareEnv := env.prepareEnv()
	prepareEnv.Environment = baseEnvironment
	tool := NewShell()
	call, err := tool.Prepare(context.Background(), mustRawJSON(t, shellInputArgs{
		Command: "go test ./...", Cwd: ".",
	}), prepareEnv)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if call.Preview.Kind != PreviewCommand || !strings.Contains(call.Preview.Body, "go test ./...") || !strings.Contains(call.Preview.Body, "10m0s") {
		t.Fatalf("unexpected preview: %#v", call.Preview)
	}
	if strings.Contains(string(call.CanonicalArg), "visible-value") || strings.Contains(string(call.CanonicalArg), "secret-value") ||
		strings.Contains(call.Preview.Body, "visible-value") || strings.Contains(call.Preview.Body, "secret-value") {
		t.Fatal("prepared call disclosed an environment value")
	}
	if strings.Contains(call.Preview.Body, "MENGDIE_TEST_SECRET_TOKEN") {
		t.Fatal("secret-like environment name was inherited without explicit permission")
	}
	if !strings.Contains(call.Preview.Body, "MENGDIE_TEST_SAFE") {
		t.Fatal("safe environment name missing from preview")
	}
	var prepared preparedShellArgs
	if err := json.Unmarshal(call.CanonicalArg, &prepared); err != nil {
		t.Fatal(err)
	}
	canonicalRoot := env.guard.Root()
	if prepared.Cwd != canonicalRoot || !filepath.IsAbs(prepared.Shell) || prepared.TimeoutMilliseconds != buildShellTimeout.Milliseconds() {
		t.Fatalf("unexpected prepared args: %#v", prepared)
	}
	if len(call.Paths) != 1 || call.Paths[0].Path != canonicalRoot || call.Paths[0].Sensitive {
		t.Fatalf("unexpected paths: %#v", call.Paths)
	}
}

func TestShellExplicitEnvironmentPermissionIsVisibleButValueIsNot(t *testing.T) {
	env := newToolTestEnv(t)
	prepareEnv := env.prepareEnv()
	prepareEnv.Environment = append(os.Environ(), "MENGDIE_TEST_SECRET_TOKEN=secret-value")
	prepareEnv.AllowedEnvironment = []string{"MENGDIE_TEST_SECRET_TOKEN"}
	call, err := NewShell().Prepare(context.Background(), mustRawJSON(t, shellInputArgs{Command: shellEchoCommand("ok")}), prepareEnv)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !strings.Contains(call.Preview.Body, "MENGDIE_TEST_SECRET_TOKEN") || strings.Contains(call.Preview.Body, "secret-value") || strings.Contains(string(call.CanonicalArg), "secret-value") {
		t.Fatalf("unsafe approval preview: %s", call.Preview.Body)
	}
}

func TestShellMarksSensitiveWorkingDirectory(t *testing.T) {
	env := newToolTestEnv(t)
	if err := os.Mkdir(filepath.Join(env.root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	call := prepareCall(t, NewShell(), env, mustJSON(t, shellInputArgs{
		Command: shellEchoCommand("ok"), Cwd: ".git",
	}))
	if len(call.Paths) != 1 || !call.Paths[0].Sensitive {
		t.Fatalf("sensitive cwd not marked: %#v", call.Paths)
	}
}

func TestShellExecuteReturnsOutputExitCodeAndFilteredEnvironment(t *testing.T) {
	env := newToolTestEnv(t)
	baseEnvironment := append(os.Environ(), "MENGDIE_TEST_SECRET_TOKEN=must-not-leak")
	prepareEnv := env.prepareEnv()
	prepareEnv.Environment = baseEnvironment
	tool := NewShell()
	call, err := tool.Prepare(context.Background(), mustRawJSON(t, shellOutputCommand()), prepareEnv)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	execEnv := env.execEnv()
	execEnv.Environment = baseEnvironment
	result, err := tool.Execute(context.Background(), call, capabilityFor(call), execEnv)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"你好", "错误", "filtered"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output missing %q: %s", want, result.Output)
		}
	}
	if strings.Contains(result.Output, "must-not-leak") {
		t.Fatal("filtered environment value reached child process")
	}
	if result.Metadata["exit_code"] != "7" || result.Metadata["status"] != "completed" || result.Metadata["cwd"] != env.guard.Root() {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
	}
}

func TestShellMissingCapabilityHasNoSideEffects(t *testing.T) {
	env := newToolTestEnv(t)
	marker := filepath.Join(env.root, "marker.txt")
	tool := NewShell()
	call := prepareCall(t, tool, env, mustJSON(t, shellWriteFileCommand(marker)))
	if _, err := tool.Execute(context.Background(), call, Capability{}, env.execEnv()); !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("Execute() error = %v, want ErrCapabilityMissing", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command ran without capability: %v", err)
	}
}

func TestShellRejectsEnvironmentChangedAfterApproval(t *testing.T) {
	env := newToolTestEnv(t)
	marker := filepath.Join(env.root, "marker.txt")
	prepareEnv := env.prepareEnv()
	prepareEnv.Environment = append(os.Environ(), "MENGDIE_TEST_SAFE=before")
	tool := NewShell()
	call, err := tool.Prepare(context.Background(), mustRawJSON(t, shellWriteFileCommand(marker)), prepareEnv)
	if err != nil {
		t.Fatal(err)
	}
	execEnv := env.execEnv()
	execEnv.Environment = append(os.Environ(), "MENGDIE_TEST_SAFE=after")
	if _, err := tool.Execute(context.Background(), call, capabilityFor(call), execEnv); !errors.Is(err, ErrShellEnvironmentChanged) {
		t.Fatalf("Execute() error = %v, want ErrShellEnvironmentChanged", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command ran after environment changed: %v", err)
	}
}

func TestShellTimeoutCancelsManagedProcess(t *testing.T) {
	env := newToolTestEnv(t)
	tool := NewShell()
	call := prepareCall(t, tool, env, mustJSON(t, shellInputArgs{
		Command: shellSleepCommand(), Timeout: "1s",
	}))
	started := time.Now()
	result, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v, want DeadlineExceeded", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatalf("timeout took too long: %s", time.Since(started))
	}
	if result == nil || result.Metadata["status"] != "timeout" || result.Metadata["forced_cleanup"] != "true" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestShellRejectsCwdSymlinkEscapeAfterApproval(t *testing.T) {
	env := newToolTestEnv(t)
	approvedDir := filepath.Join(env.root, "work")
	if err := os.Mkdir(approvedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideMarker := filepath.Join(outside, "marker.txt")
	tool := NewShell()
	call := prepareCall(t, tool, env, mustJSON(t, shellInputArgs{
		Command: shellWriteFileCommand("marker.txt").Command, Cwd: "work",
	}))
	if err := os.Remove(approvedDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, approvedDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv()); err == nil {
		t.Fatal("Execute() followed cwd symlink outside the project")
	}
	if _, err := os.Stat(outsideMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside marker was created: %v", err)
	}
}

func TestShellProvidesNoInteractiveStdin(t *testing.T) {
	env := newToolTestEnv(t)
	tool := NewShell()
	call := prepareCall(t, tool, env, mustJSON(t, shellInputArgs{
		Command: shellReadStdinCommand(), Timeout: "2s",
	}))
	started := time.Now()
	result, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("interactive command waited for stdin: %s", time.Since(started))
	}
	if result.Metadata["status"] != "completed" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestShellBoundsLargeOutput(t *testing.T) {
	env := newToolTestEnv(t)
	tool := NewShell()
	call := prepareCall(t, tool, env, mustJSON(t, shellInputArgs{Command: shellLargeOutputCommand()}))
	result := executeCall(t, tool, env, call)
	if !result.Truncated || !strings.Contains(result.Output, "truncated") {
		t.Fatalf("large output was not marked truncated: %#v", result)
	}
	if len(result.Output) > DefaultToolOutputBytes {
		t.Fatalf("output size = %d, want <= %d", len(result.Output), DefaultToolOutputBytes)
	}
}

func TestShellSanitizesTerminalControlSequences(t *testing.T) {
	env := newToolTestEnv(t)
	tool := NewShell()
	call := prepareCall(t, tool, env, mustJSON(t, shellInputArgs{Command: shellControlOutputCommand()}))
	result := executeCall(t, tool, env, call)
	if strings.ContainsRune(result.Output, 0x1b) || !strings.Contains(result.Output, `\x1b[31mred`) {
		t.Fatalf("control sequence was not escaped: %q", result.Output)
	}
}

func TestShellPrepareRejectsUnsafeInputs(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "file.txt", "not a directory")
	tool := NewShell()
	tests := []json.RawMessage{
		json.RawMessage(`{"command":""}`),
		json.RawMessage(`{"command":"echo ok\rhidden"}`),
		json.RawMessage(`{"command":"echo \u001b[31mred"}`),
		json.RawMessage(`{"command":"echo ok","timeout":"500ms"}`),
		json.RawMessage(`{"command":"echo ok","timeout":"11m"}`),
		json.RawMessage(`{"command":"echo ok","timeout":"invalid"}`),
		json.RawMessage(`{"command":"echo ok","cwd":"../outside"}`),
		json.RawMessage(`{"command":"echo ok","cwd":"file.txt"}`),
		json.RawMessage(`{"command":"echo ok","unknown":true}`),
	}
	for _, raw := range tests {
		if _, err := tool.Prepare(context.Background(), raw, env.prepareEnv()); err == nil {
			t.Errorf("Prepare(%s) succeeded, want error", raw)
		}
	}
}

func TestBoundedOutputKeepsHeadAndTail(t *testing.T) {
	output := newBoundedOutput(10)
	for _, part := range []string{"abc", "defgh", "ijklmno"} {
		if _, err := output.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	text, total, truncated := output.snapshot()
	if total != 15 || !truncated || !strings.HasPrefix(text, "abcde") || !strings.HasSuffix(text, "klmno") {
		t.Fatalf("snapshot = %q total=%d truncated=%t", text, total, truncated)
	}
}

func shellOutputCommand() shellInputArgs {
	if runtime.GOOS == "windows" {
		return shellInputArgs{Command: "[Console]::Out.Write('你好 filtered=' + $(if ($null -eq $env:MENGDIE_TEST_SECRET_TOKEN) {'filtered'} else {$env:MENGDIE_TEST_SECRET_TOKEN})); [Console]::Error.Write('错误'); exit 7"}
	}
	return shellInputArgs{Command: `printf '你好 filtered=%s' "${MENGDIE_TEST_SECRET_TOKEN-filtered}"; printf '错误' >&2; exit 7`}
}

func shellEchoCommand(text string) string {
	if runtime.GOOS == "windows" {
		return "[Console]::Out.Write('" + strings.ReplaceAll(text, "'", "''") + "')"
	}
	return "printf '%s' '" + strings.ReplaceAll(text, "'", `'"'"'`) + "'"
}

func shellWriteFileCommand(path string) shellInputArgs {
	if runtime.GOOS == "windows" {
		return shellInputArgs{Command: "[System.IO.File]::WriteAllText('" + strings.ReplaceAll(path, "'", "''") + "', 'created')"}
	}
	return shellInputArgs{Command: "printf created > '" + strings.ReplaceAll(path, "'", `'"'"'`) + "'"}
}

func shellSleepCommand() string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds 30"
	}
	return "sleep 30"
}

func shellLargeOutputCommand() string {
	if runtime.GOOS == "windows" {
		return "[Console]::Out.Write('x' * 70000)"
	}
	return "printf '%070000d' 0"
}

func shellReadStdinCommand() string {
	if runtime.GOOS == "windows" {
		return "try { Read-Host | Out-Null } catch { }; exit 0"
	}
	return "read value || true"
}

func shellControlOutputCommand() string {
	if runtime.GOOS == "windows" {
		return "[Console]::Out.Write([char]27 + '[31mred')"
	}
	return "printf '\\033[31mred'"
}
