// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

func resolveShell(env []string) (Shell, error) {
	if candidate := environmentValue(env, "SHELL", false); candidate != "" {
		if shell, err := unixShell(candidate); err == nil {
			return shell, nil
		}
	}
	fallbacks := []string{"/bin/bash", "/bin/sh"}
	if runtime.GOOS == "darwin" {
		fallbacks = []string{"/bin/zsh", "/bin/bash", "/bin/sh"}
	}
	for _, candidate := range fallbacks {
		if shell, err := unixShell(candidate); err == nil {
			return shell, nil
		}
	}
	return Shell{}, fmt.Errorf("%w: expected zsh, bash, or sh", ErrShellUnavailable)
}

func unixShell(path string) (Shell, error) {
	if !filepath.IsAbs(path) {
		return Shell{}, errors.New("shell path must be absolute")
	}
	name := filepath.Base(path)
	if !slices.Contains([]string{"zsh", "bash", "sh"}, name) {
		return Shell{}, fmt.Errorf("unsupported shell %q", name)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Shell{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return Shell{}, fs.ErrPermission
	}
	return Shell{Name: name, Executable: filepath.Clean(path), PrefixArgs: []string{"-lc"}}, nil
}

func environmentValue(env []string, name string, foldCase bool) string {
	for index := len(env) - 1; index >= 0; index-- {
		key, value, ok := strings.Cut(env[index], "=")
		if !ok {
			continue
		}
		if key == name || foldCase && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}
