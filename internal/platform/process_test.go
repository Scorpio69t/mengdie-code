// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const processTreeHelperEnvironment = "GO_WANT_MENGDIE_PROCESS_HELPER"

func TestRunProcessCancellationTerminatesDescendants(t *testing.T) {
	root := t.TempDir()
	readyPath := filepath.Join(root, "parent.ready")
	gatePath := filepath.Join(root, "parent.go")
	childPIDPath := filepath.Join(root, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		result ProcessResult
		err    error
	}, 1)
	go func() {
		result, err := RunProcess(ctx, ProcessSpec{
			Executable: os.Args[0],
			Args: []string{
				"-test.run=^TestProcessTreeHelper$", "--", "parent", readyPath, gatePath, childPIDPath,
			},
			Dir:       root,
			Env:       append(os.Environ(), processTreeHelperEnvironment+"=1"),
			Stdout:    io.Discard,
			Stderr:    io.Discard,
			KillGrace: 500 * time.Millisecond,
		})
		done <- struct {
			result ProcessResult
			err    error
		}{result: result, err: err}
	}()

	waitForFile(t, readyPath, 5*time.Second)
	// The helper does not spawn its child until this gate is created, giving
	// the Windows runner time to assign the parent to its Job Object.
	if err := os.WriteFile(gatePath, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, childPIDPath, 5*time.Second)
	pidText, err := os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	select {
	case outcome := <-done:
		if !errors.Is(outcome.err, context.Canceled) {
			t.Fatalf("RunProcess() error = %v, want context.Canceled", outcome.err)
		}
		if !outcome.result.ForcedCleanup {
			t.Fatalf("RunProcess() result = %#v, want forced cleanup", outcome.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunProcess() did not return after cancellation")
	}

	deadline := time.Now().Add(5 * time.Second)
	for processAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if processAlive(childPID) {
		t.Fatalf("descendant process %d survived cancellation", childPID)
	}
}

func TestRunProcessRejectsCancelledContextBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := RunProcess(ctx, ProcessSpec{})
	if !errors.Is(err, context.Canceled) || result.ExitCode != -1 {
		t.Fatalf("RunProcess() = %#v, %v", result, err)
	}
}

func TestProcessTreeHelper(t *testing.T) {
	if os.Getenv(processTreeHelperEnvironment) != "1" {
		return
	}
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || len(os.Args) <= separator+1 {
		os.Exit(90)
	}
	args := os.Args[separator+1:]
	switch args[0] {
	case "parent":
		if len(args) != 4 {
			os.Exit(91)
		}
		if err := os.WriteFile(args[1], []byte("ready"), 0o600); err != nil {
			os.Exit(92)
		}
		for {
			if _, err := os.Stat(args[2]); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$", "--", "child")
		child.Env = append(os.Environ(), processTreeHelperEnvironment+"=1")
		if err := child.Start(); err != nil {
			os.Exit(93)
		}
		if err := os.WriteFile(args[3], []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(94)
		}
		for {
			time.Sleep(time.Second)
		}
	case "child":
		prepareChildProcessHelper()
		for {
			time.Sleep(time.Second)
		}
	default:
		os.Exit(95)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
