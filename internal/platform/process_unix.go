// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func runProcess(ctx context.Context, spec ProcessSpec) (ProcessResult, error) {
	if err := validateProcessSpec(ctx, spec); err != nil {
		return ProcessResult{ExitCode: -1}, err
	}
	command := exec.Command(spec.Executable, spec.Args...)
	command.Dir = spec.Dir
	command.Env = append([]string(nil), spec.Env...)
	command.Stdin = nil
	command.Stdout = spec.Stdout
	command.Stderr = spec.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = spec.KillGrace

	started := time.Now()
	if err := command.Start(); err != nil {
		return ProcessResult{ExitCode: -1, Duration: time.Since(started)}, fmt.Errorf("start process: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()

	select {
	case waitErr := <-done:
		result, err := processResult(started, waitErr)
		result.ForcedCleanup = cleanupProcessGroup(command.Process.Pid, spec.KillGrace) || result.ForcedCleanup
		return result, err
	case <-ctx.Done():
	}

	// Prefer a graceful group shutdown, then force-kill any descendants that
	// ignored TERM. Negative pid addresses the complete process group.
	terminateErr := signalProcessGroup(command.Process.Pid, syscall.SIGTERM)
	timer := time.NewTimer(spec.KillGrace)
	defer timer.Stop()
	select {
	case waitErr := <-done:
		result, resultErr := processResult(started, waitErr)
		_ = cleanupProcessGroup(command.Process.Pid, spec.KillGrace)
		result.ForcedCleanup = true
		return result, errors.Join(ctx.Err(), ignoreNoProcess(terminateErr), resultErr)
	case <-timer.C:
		killErr := signalProcessGroup(command.Process.Pid, syscall.SIGKILL)
		waitErr := <-done
		result, resultErr := processResult(started, waitErr)
		result.ForcedCleanup = true
		return result, errors.Join(ctx.Err(), ignoreNoProcess(terminateErr), ignoreNoProcess(killErr), resultErr)
	}
}

func validateProcessSpec(ctx context.Context, spec ProcessSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(spec.Executable) || !filepath.IsAbs(spec.Dir) {
		return errors.New("process executable and directory must be absolute")
	}
	if spec.KillGrace <= 0 {
		return errors.New("process kill grace must be positive")
	}
	return nil
}

func cleanupProcessGroup(pid int, grace time.Duration) bool {
	if err := signalProcessGroup(pid, syscall.Signal(0)); err != nil {
		return false
	}
	_ = signalProcessGroup(pid, syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	<-timer.C
	_ = signalProcessGroup(pid, syscall.SIGKILL)
	return true
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	return syscall.Kill(-pid, signal)
}

func ignoreNoProcess(err error) error {
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
