// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package evaluation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const outputLimit = 64 << 10

// Result is the machine-readable outcome of one evaluation suite run.
type Result struct {
	SchemaVersion  int          `json:"schema_version"`
	SuiteID        string       `json:"suite_id"`
	Mode           string       `json:"mode"`
	StartedAt      time.Time    `json:"started_at"`
	DurationMillis int64        `json:"duration_ms"`
	Passed         bool         `json:"passed"`
	PassedTasks    int          `json:"passed_tasks"`
	FailedTasks    int          `json:"failed_tasks"`
	Tasks          []TaskResult `json:"tasks"`
}

// TaskResult is the bounded diagnostic output of one fixture verifier.
type TaskResult struct {
	ID               string `json:"id"`
	Passed           bool   `json:"passed"`
	ExpectedExitCode int    `json:"expected_exit_code"`
	ActualExitCode   int    `json:"actual_exit_code"`
	DurationMillis   int64  `json:"duration_ms"`
	Stdout           string `json:"stdout,omitempty"`
	Stderr           string `json:"stderr,omitempty"`
	Error            string `json:"error,omitempty"`
}

// RunBaseline executes each verifier against an untouched temporary copy.
func RunBaseline(ctx context.Context, manifestPath string) (Result, error) {
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return Result{}, err
	}
	fixtureRoot, err := resolveFixtureRoot(manifestPath, manifest.FixtureRoot)
	if err != nil {
		return Result{}, err
	}

	started := time.Now().UTC()
	result := Result{
		SchemaVersion: SchemaVersion,
		SuiteID:       manifest.ID,
		Mode:          "baseline",
		StartedAt:     started,
		Passed:        true,
		Tasks:         make([]TaskResult, 0, len(manifest.Tasks)),
	}

	for _, task := range manifest.Tasks {
		taskResult := runBaselineTask(ctx, fixtureRoot, task)
		result.Tasks = append(result.Tasks, taskResult)
		if taskResult.Passed {
			result.PassedTasks++
		} else {
			result.Passed = false
			result.FailedTasks++
		}
	}
	result.DurationMillis = time.Since(started).Milliseconds()
	return result, nil
}

func runBaselineTask(ctx context.Context, fixtureRoot string, task Task) (result TaskResult) {
	started := time.Now()
	result = TaskResult{
		ID:               task.ID,
		ExpectedExitCode: task.Baseline.ExpectedExitCode,
		ActualExitCode:   -1,
	}
	defer func() {
		result.DurationMillis = time.Since(started).Milliseconds()
	}()

	source, err := resolveFixture(fixtureRoot, task.Fixture)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	tempRoot, err := os.MkdirTemp("", "mengdie-eval-*")
	if err != nil {
		result.Error = fmt.Sprintf("create temporary fixture: %v", err)
		return result
	}
	defer os.RemoveAll(tempRoot)

	workDir := filepath.Join(tempRoot, "workspace")
	if err := copyTree(source, workDir); err != nil {
		result.Error = err.Error()
		return result
	}

	timeout, err := task.Verify.duration()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(commandContext, task.Verify.Command[0], task.Verify.Command[1:]...)
	command.Dir = workDir
	command.Env = withEnvironment(os.Environ(), "GOWORK=off")
	stdout := newLimitWriter(outputLimit)
	stderr := newLimitWriter(outputLimit)
	command.Stdout = stdout
	command.Stderr = stderr

	runErr := command.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.ActualExitCode = exitCode(runErr)
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
		result.Error = fmt.Sprintf("verification timed out after %s", timeout)
		return result
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			result.Error = runErr.Error()
			return result
		}
	}
	result.Passed = result.ActualExitCode == result.ExpectedExitCode
	if !result.Passed {
		result.Error = fmt.Sprintf("exit code %d, expected %d", result.ActualExitCode, result.ExpectedExitCode)
	}
	return result
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return fmt.Errorf("resolve fixture entry: %w", err)
		}
		target := filepath.Join(destination, relative)

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect fixture entry %q: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture entry %q is a symbolic link", relative)
		}
		if entry.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("create fixture directory %q: %w", relative, err)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("fixture entry %q is not a regular file", relative)
		}
		if err := copyFile(path, target, info.Mode().Perm()); err != nil {
			return fmt.Errorf("copy fixture entry %q: %w", relative, err)
		}
		return nil
	})
}

func copyFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

func withEnvironment(environment []string, value string) []string {
	key := value[:bytes.IndexByte([]byte(value), '=')+1]
	filtered := slices.DeleteFunc(slices.Clone(environment), func(item string) bool {
		return len(item) >= len(key) && strings.EqualFold(item[:len(key)], key)
	})
	return append(filtered, value)
}

type limitWriter struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func newLimitWriter(limit int) *limitWriter {
	return &limitWriter{remaining: limit}
}

func (w *limitWriter) Write(data []byte) (int, error) {
	originalLength := len(data)
	if len(data) > w.remaining {
		data = data[:w.remaining]
		w.truncated = true
	}
	if len(data) > 0 {
		_, _ = w.buffer.Write(data)
		w.remaining -= len(data)
	}
	return originalLength, nil
}

func (w *limitWriter) String() string {
	if w.truncated {
		return w.buffer.String() + "\n[output truncated]"
	}
	return w.buffer.String()
}