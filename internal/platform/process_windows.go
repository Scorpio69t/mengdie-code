// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func runProcess(ctx context.Context, spec ProcessSpec) (result ProcessResult, err error) {
	if err := validateProcessSpec(ctx, spec); err != nil {
		return ProcessResult{ExitCode: -1}, err
	}
	job, err := createKillOnCloseJob()
	if err != nil {
		return ProcessResult{ExitCode: -1}, fmt.Errorf("create process job: %w", err)
	}
	defer func() {
		if closeErr := windows.CloseHandle(job); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close process job: %w", closeErr))
		}
	}()

	command := exec.Command(spec.Executable, spec.Args...)
	command.Dir = spec.Dir
	command.Env = append([]string(nil), spec.Env...)
	command.Stdin = nil
	command.Stdout = spec.Stdout
	command.Stderr = spec.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	command.WaitDelay = spec.KillGrace

	started := time.Now()
	if err := command.Start(); err != nil {
		return ProcessResult{ExitCode: -1, Duration: time.Since(started)}, fmt.Errorf("start process: %w", err)
	}
	process, openErr := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if openErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return ProcessResult{ExitCode: -1, Duration: time.Since(started)}, fmt.Errorf("open process for job assignment: %w", openErr)
	}
	assignErr := windows.AssignProcessToJobObject(job, process)
	closeProcessErr := windows.CloseHandle(process)
	if err := errors.Join(assignErr, closeProcessErr); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return ProcessResult{ExitCode: -1, Duration: time.Since(started)}, fmt.Errorf("assign process job: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case waitErr := <-done:
		return processResult(started, waitErr)
	case <-ctx.Done():
		terminateErr := windows.TerminateJobObject(job, 1)
		waitErr := <-done
		result, resultErr := processResult(started, waitErr)
		result.ForcedCleanup = true
		return result, errors.Join(ctx.Err(), terminateErr, resultErr)
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

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}
