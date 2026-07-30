// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package project locates repository-scoped resources.
package project

import (
	"fmt"
	"os"
	"path/filepath"
)

// FindRoot walks upward from start until it finds a .git entry. When start is
// not inside a Git worktree, the normalized starting directory is returned.
func FindRoot(start string) (string, error) {
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get working directory: %w", err)
		}
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat project path: %w", err)
	}
	if !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	origin := filepath.Clean(abs)

	for current := origin; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect Git marker: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return origin, nil
		}
	}
}

