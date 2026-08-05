// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package platform

import (
	"errors"
	"os/signal"
	"syscall"
)

func prepareChildProcessHelper() {
	signal.Ignore(syscall.SIGTERM)
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
