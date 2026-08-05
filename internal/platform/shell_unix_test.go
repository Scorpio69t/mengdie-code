// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package platform

import "testing"

func TestResolveShellUnixUsesSupportedAbsoluteShell(t *testing.T) {
	shell, err := ResolveShell([]string{"SHELL=/bin/sh"})
	if err != nil {
		t.Fatalf("ResolveShell() error = %v", err)
	}
	if shell.Name != "sh" || shell.Executable != "/bin/sh" {
		t.Fatalf("ResolveShell() = %#v", shell)
	}
}

func TestResolveShellUnixRejectsUnsupportedShellAndFallsBack(t *testing.T) {
	shell, err := ResolveShell([]string{"SHELL=/usr/bin/fish"})
	if err != nil {
		t.Fatalf("ResolveShell() fallback error = %v", err)
	}
	if shell.Name != "bash" && shell.Name != "sh" && shell.Name != "zsh" {
		t.Fatalf("ResolveShell() = %#v", shell)
	}
}
