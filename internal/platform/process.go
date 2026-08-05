// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

// ErrShellUnavailable means no supported shell executable is available.
var ErrShellUnavailable = errors.New("supported shell unavailable")

// Shell describes the concrete platform shell selected for one approved call.
type Shell struct {
	Name       string
	Executable string
	PrefixArgs []string
}

// ProcessSpec is an already authorized process invocation. Executable and Dir
// must be absolute; stdin is intentionally unavailable.
type ProcessSpec struct {
	Executable string
	Args       []string
	Dir        string
	Env        []string
	Stdout     io.Writer
	Stderr     io.Writer
	KillGrace  time.Duration
}

// ProcessResult records stable execution evidence independently of shell UI.
type ProcessResult struct {
	ExitCode      int
	Duration      time.Duration
	ForcedCleanup bool
}

func processResult(started time.Time, waitErr error) (ProcessResult, error) {
	result := ProcessResult{ExitCode: 0, Duration: time.Since(started)}
	if waitErr == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		result.ForcedCleanup = true
		return result, nil
	}
	result.ExitCode = -1
	return result, waitErr
}

// ResolveShell selects the platform-default non-interactive shell from env.
func ResolveShell(env []string) (Shell, error) { return resolveShell(env) }

// RunProcess starts a process in the platform's process-tree container and
// guarantees cancellation targets the complete managed tree.
func RunProcess(ctx context.Context, spec ProcessSpec) (ProcessResult, error) {
	return runProcess(ctx, spec)
}
