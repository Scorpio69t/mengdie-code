// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveShellWindowsPrefersPowerShellSeven(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	for _, path := range []string{
		filepath.Join(first, "powershell.exe"),
		filepath.Join(second, "pwsh.exe"),
	} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	shell, err := ResolveShell([]string{"Path=" + first + string(os.PathListSeparator) + second})
	if err != nil {
		t.Fatalf("ResolveShell() error = %v", err)
	}
	if shell.Name != "pwsh" || shell.Executable != filepath.Join(second, "pwsh.exe") {
		t.Fatalf("ResolveShell() = %#v", shell)
	}
}

func TestResolveShellWindowsRejectsRelativePATHEntries(t *testing.T) {
	if _, err := ResolveShell([]string{"PATH=relative"}); err == nil {
		t.Fatal("ResolveShell() accepted a relative PATH entry")
	}
}
