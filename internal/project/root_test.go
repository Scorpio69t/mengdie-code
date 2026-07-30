// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindRootFromNestedDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "internal", "agent")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := FindRoot(nested)
	if err != nil {
		t.Fatalf("FindRoot() error = %v", err)
	}
	if got != root {
		t.Fatalf("FindRoot() = %q, want %q", got, root)
	}
}

func TestFindRootFallsBackToStart(t *testing.T) {
	start := t.TempDir()
	got, err := FindRoot(start)
	if err != nil {
		t.Fatalf("FindRoot() error = %v", err)
	}
	if got != start {
		t.Fatalf("FindRoot() = %q, want %q", got, start)
	}
}
