// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

const (
	defaultShellTimeout  = 2 * time.Minute
	buildShellTimeout    = 10 * time.Minute
	maxShellTimeout      = 10 * time.Minute
	minShellTimeout      = time.Second
	maxShellCommandBytes = 16 << 10
	processKillGrace     = 750 * time.Millisecond
)

const shellSchema = `{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "由平台默认 shell 执行的完整非交互命令"},
    "cwd": {"type": "string", "description": "项目根内的工作目录，默认项目根"},
    "timeout": {"type": "string", "description": "Go duration，例如 30s 或 5m；上限 10m"}
  },
  "required": ["command"],
  "additionalProperties": false
}`

type shellInputArgs struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	Timeout string `json:"timeout"`
}

type preparedShellArgs struct {
	Command             string               `json:"command"`
	Cwd                 string               `json:"cwd"`
	TimeoutMilliseconds int64                `json:"timeout_ms"`
	Shell               string               `json:"shell"`
	ShellArgs           []string             `json:"shell_args"`
	AllowedEnvironment  []string             `json:"allowed_environment"`
	Environment         []environmentBinding `json:"environment"`
}

// NewShell builds the M1 non-interactive, process-tree-managed shell tool.
func NewShell() Tool { return shellTool{} }

type shellTool struct{}

func (shellTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "shell",
		Description: "在受控本地执行边界运行非交互 shell 命令；显示 cwd、超时、shell 与环境变量名",
		InputSchema: json.RawMessage(shellSchema),
		Effects:     []Effect{EffectExecute},
	}
}

func (shellTool) Prepare(ctx context.Context, raw json.RawMessage, env PrepareEnv) (*PreparedCall, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var input shellInputArgs
	if err := decodeArgs(raw, &input); err != nil {
		return nil, err
	}
	if err := validateShellCommand(input.Command); err != nil {
		return nil, err
	}
	timeout, err := resolveShellTimeout(input.Command, input.Timeout)
	if err != nil {
		return nil, err
	}
	cwd := input.Cwd
	if strings.TrimSpace(cwd) == "" {
		cwd = "."
	}
	resolved, err := env.Guard.Resolve(cwd, platform.AccessRead)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved.Path)
	if err != nil {
		return nil, fmt.Errorf("shell: inspect cwd: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("shell: cwd %q is not a directory", cwd)
	}
	baseEnvironment := environmentOrCurrent(env.Environment)
	shell, err := platform.ResolveShell(baseEnvironment)
	if err != nil {
		return nil, fmt.Errorf("shell: %w", err)
	}
	approvedEnvironment, bindings, allowed, err := buildApprovedEnvironment(baseEnvironment, env.AllowedEnvironment)
	if err != nil {
		return nil, err
	}
	_ = approvedEnvironment // Values are intentionally excluded from PreparedCall.
	prepared := preparedShellArgs{
		Command:             input.Command,
		Cwd:                 resolved.Path,
		TimeoutMilliseconds: timeout.Milliseconds(),
		Shell:               shell.Executable,
		ShellArgs:           append([]string(nil), shell.PrefixArgs...),
		AllowedEnvironment:  allowed,
		Environment:         bindings,
	}
	preparedRaw, err := json.Marshal(prepared)
	if err != nil {
		return nil, fmt.Errorf("shell: encode prepared arguments: %w", err)
	}
	preview := shellPreview(shell.Name, prepared, bindings)
	return PrepareCall(env.CallID, "shell", preparedRaw,
		[]Effect{EffectExecute},
		[]PathResource{{Path: resolved.Path, Sensitive: resolved.Sensitive}},
		Preview{Kind: PreviewCommand, Title: "执行本地命令", Body: preview},
		nil,
	)
}

func (shellTool) Execute(ctx context.Context, call *PreparedCall, cap Capability, env ExecEnv) (*ToolResult, error) {
	if err := CheckCapability(ctx, call, cap, env); err != nil {
		return nil, err
	}
	var prepared preparedShellArgs
	if err := decodeArgs(call.CanonicalArg, &prepared); err != nil {
		return nil, err
	}
	if err := validatePreparedShell(prepared); err != nil {
		return nil, err
	}
	resolved, err := env.Guard.Resolve(prepared.Cwd, platform.AccessRead)
	if err != nil {
		return nil, err
	}
	if err := ensureSamePreparedPath(call, resolved.Path); err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved.Path)
	if err != nil || !info.IsDir() {
		return nil, &PreconditionError{Path: resolved.Path, Reason: "approved working directory is unavailable"}
	}
	if err := validateShellExecutable(prepared.Shell); err != nil {
		return nil, &PreconditionError{Path: prepared.Shell, Reason: err.Error()}
	}
	processEnvironment, bindings, _, err := buildApprovedEnvironment(environmentOrCurrent(env.Environment), prepared.AllowedEnvironment)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(bindings, prepared.Environment) {
		return nil, ErrShellEnvironmentChanged
	}

	stdout := newBoundedOutput(DefaultToolOutputBytes)
	stderr := newBoundedOutput(DefaultToolOutputBytes)
	runContext, cancel := context.WithTimeout(ctx, time.Duration(prepared.TimeoutMilliseconds)*time.Millisecond)
	defer cancel()
	result, runErr := platform.RunProcess(runContext, platform.ProcessSpec{
		Executable: prepared.Shell,
		Args:       append(append([]string(nil), prepared.ShellArgs...), prepared.Command),
		Dir:        resolved.Path,
		Env:        processEnvironment,
		Stdout:     stdout,
		Stderr:     stderr,
		KillGrace:  processKillGrace,
	})
	toolResult := shellResult(prepared, result, stdout, stderr, runErr)
	if runErr != nil {
		return toolResult, classifyShellRunError(runErr)
	}
	return toolResult, nil
}

func validateShellCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("shell: command is required")
	}
	if len(command) > maxShellCommandBytes {
		return fmt.Errorf("shell: command exceeds %d-byte limit", maxShellCommandBytes)
	}
	if !utf8.ValidString(command) || strings.IndexByte(command, 0) >= 0 {
		return errors.New("shell: command must be UTF-8 text without NUL bytes")
	}
	for _, char := range command {
		if char < 0x20 && char != '\n' && char != '\t' {
			return fmt.Errorf("shell: command contains unsafe control character U+%04X", char)
		}
		if char == 0x7f || char == 0x1b {
			return fmt.Errorf("shell: command contains unsafe control character U+%04X", char)
		}
	}
	return nil
}

func resolveShellTimeout(command, raw string) (time.Duration, error) {
	timeout := defaultTimeoutForCommand(command)
	if raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("shell: invalid timeout: %w", err)
		}
		timeout = parsed
	}
	if timeout < minShellTimeout || timeout > maxShellTimeout {
		return 0, fmt.Errorf("shell: timeout must be between %s and %s", minShellTimeout, maxShellTimeout)
	}
	return timeout, nil
}

func defaultTimeoutForCommand(command string) time.Duration {
	fields := strings.Fields(strings.ToLower(command))
	if len(fields) == 0 {
		return defaultShellTimeout
	}
	first := fields[0]
	second := ""
	if len(fields) > 1 {
		second = fields[1]
	}
	if first == "make" || slices.Contains([]string{"test", "build", "vet", "package", "check"}, second) &&
		slices.Contains([]string{"go", "cargo", "npm", "pnpm", "yarn", "dotnet", "mvn", "gradle", "./gradlew", "gradlew"}, first) {
		return buildShellTimeout
	}
	return defaultShellTimeout
}

func validatePreparedShell(prepared preparedShellArgs) error {
	if err := validateShellCommand(prepared.Command); err != nil {
		return err
	}
	if !filepath.IsAbs(prepared.Cwd) || !filepath.IsAbs(prepared.Shell) {
		return errors.New("shell: prepared cwd and shell must be absolute")
	}
	timeout := time.Duration(prepared.TimeoutMilliseconds) * time.Millisecond
	if timeout < minShellTimeout || timeout > maxShellTimeout {
		return errors.New("shell: prepared timeout is outside limits")
	}
	if len(prepared.ShellArgs) == 0 || len(prepared.Environment) == 0 {
		return errors.New("shell: prepared execution contract is incomplete")
	}
	return nil
}

func validateShellExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("shell executable is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return errors.New("shell executable is not executable")
	}
	return nil
}

func shellPreview(shellName string, prepared preparedShellArgs, bindings []environmentBinding) string {
	names := make([]string, len(bindings))
	for index, binding := range bindings {
		names[index] = binding.Name
	}
	return fmt.Sprintf("Security: 受控本地执行（不是强沙箱）\nShell: %s (%s)\nShell flags: %s\nCWD: %s\nTimeout: %s\nInherited environment names: %s\n\nCommand:\n%s",
		shellName, prepared.Shell, strings.Join(prepared.ShellArgs, " "), prepared.Cwd,
		time.Duration(prepared.TimeoutMilliseconds)*time.Millisecond,
		strings.Join(names, ", "), prepared.Command)
}

func shellResult(prepared preparedShellArgs, process platform.ProcessResult, stdout, stderr *boundedOutput, cause error) *ToolResult {
	stdoutText, stdoutBytes, stdoutTruncated := stdout.snapshot()
	stderrText, stderrBytes, stderrTruncated := stderr.snapshot()
	stdoutText = sanitizeShellOutput(stdoutText)
	stderrText = sanitizeShellOutput(stderrText)
	output := fmt.Sprintf("stdout:\n%s\n\nstderr:\n%s", emptyOutput(stdoutText), emptyOutput(stderrText))
	// Reserve room for the truncation marker because the shared helper's max
	// parameter budgets retained content rather than the marker itself.
	output, combinedTruncated := truncateHeadTail(output, DefaultToolOutputBytes-128)
	status := "completed"
	if errors.Is(cause, context.DeadlineExceeded) {
		status = "timeout"
	} else if errors.Is(cause, context.Canceled) {
		status = "cancelled"
	}
	return &ToolResult{
		Output:    output,
		Truncated: stdoutTruncated || stderrTruncated || combinedTruncated,
		Metadata: map[string]string{
			"status":         status,
			"exit_code":      fmt.Sprintf("%d", process.ExitCode),
			"duration_ms":    fmt.Sprintf("%d", process.Duration.Milliseconds()),
			"cwd":            prepared.Cwd,
			"shell":          prepared.Shell,
			"stdout_bytes":   fmt.Sprintf("%d", stdoutBytes),
			"stderr_bytes":   fmt.Sprintf("%d", stderrBytes),
			"forced_cleanup": fmt.Sprintf("%t", process.ForcedCleanup),
		},
	}
}

func emptyOutput(output string) string {
	if output == "" {
		return "<empty>"
	}
	return output
}

func classifyShellRunError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("shell: command timed out: %w", err)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("shell: command cancelled: %w", err)
	default:
		return fmt.Errorf("shell: process execution failed: %w", err)
	}
}
