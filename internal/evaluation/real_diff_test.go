// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package evaluation

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRealDiffRebuildsOnlyExactOrdinaryFiles(t *testing.T) {
	agentRoot := t.TempDir()
	finalRoot := t.TempDir()
	for _, root := range []string{agentRoot, finalRoot} {
		if err := os.WriteFile(filepath.Join(root, "context.go"), []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := captureRealSnapshot(agentRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentRoot, "context.go"), []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentRoot, "context_test.go"), []byte("new test"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := captureRealSnapshot(agentRoot)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := allowedRealChanges(before, after, []string{"context.go", "context_test.go"})
	if err != nil || len(changes) != 2 {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	if err := rebuildRealChanges(agentRoot, finalRoot, before, after, changes); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"context.go": "after", "context_test.go": "new test"} {
		got, err := os.ReadFile(filepath.Join(finalRoot, path))
		if err != nil || string(got) != want {
			t.Fatalf("%s=%q err=%v", path, got, err)
		}
	}
}

func TestRealDiffRejectsOutsideAddDeleteAndModeChange(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(string) error
	}{
		{"outside addition", func(root string) error { return os.WriteFile(filepath.Join(root, "secret.txt"), []byte("x"), 0o600) }},
		{"outside deletion", func(root string) error { return os.Remove(filepath.Join(root, "outside.txt")) }},
		{"allowed mode change", func(root string) error { return os.Chmod(filepath.Join(root, "context.go"), 0o700) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"context.go", "outside.txt"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("before"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := captureRealSnapshot(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(root); err != nil {
				t.Fatal(err)
			}
			after, err := captureRealSnapshot(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := allowedRealChanges(before, after, []string{"context.go"}); err == nil && (test.name != "allowed mode change" || runtime.GOOS != "windows") {
				t.Fatal("unsafe change passed exact allowlist")
			}
		})
	}
}

func TestRealDiffRejectsChangedSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "context.go"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := captureRealSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "context.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, "context.go")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	after, err := captureRealSnapshot(root)
	if runtime.GOOS == "windows" {
		if err == nil {
			t.Fatal("Windows reparse point entered snapshot")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := allowedRealChanges(before, after, []string{"context.go"}); err == nil {
		t.Fatal("changed symlink passed allowlist")
	}
}
