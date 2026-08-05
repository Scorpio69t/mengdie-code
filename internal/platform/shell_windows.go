// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func resolveShell(env []string) (Shell, error) {
	pathValue := environmentValue(env, "PATH", true)
	for _, candidate := range []struct {
		name string
		file string
	}{
		{name: "pwsh", file: "pwsh.exe"},
		{name: "powershell", file: "powershell.exe"},
	} {
		for _, dir := range filepath.SplitList(pathValue) {
			if !filepath.IsAbs(dir) {
				continue
			}
			path := filepath.Join(dir, candidate.file)
			info, err := os.Stat(path)
			if err == nil && info.Mode().IsRegular() {
				absolute, err := filepath.Abs(path)
				if err != nil {
					return Shell{}, err
				}
				return Shell{
					Name:       candidate.name,
					Executable: filepath.Clean(absolute),
					PrefixArgs: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"},
				}, nil
			}
		}
	}
	return Shell{}, fmt.Errorf("%w: expected pwsh.exe or powershell.exe on PATH", ErrShellUnavailable)
}

func environmentValue(env []string, name string, _ bool) string {
	for index := len(env) - 1; index >= 0; index-- {
		key, value, ok := cutEnvironment(env[index])
		if ok && equalEnvironmentName(key, name) {
			return value
		}
	}
	return ""
}

func cutEnvironment(entry string) (string, string, bool) {
	for index := 1; index < len(entry); index++ {
		if entry[index] == '=' {
			return entry[:index], entry[index+1:], true
		}
	}
	return "", "", false
}

func equalEnvironmentName(left, right string) bool {
	return strings.EqualFold(left, right)
}
