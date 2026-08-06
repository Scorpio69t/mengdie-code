// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgentsOrdersUserRootAndNearestScopes(t *testing.T) {
	root := t.TempDir()
	user := t.TempDir()
	work := filepath.Join(root, "internal", "parser")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(user, "AGENTS.md"):                       "user",
		filepath.Join(root, "AGENTS.md"):                       "root",
		filepath.Join(root, "internal", "AGENTS.md"):           "internal",
		filepath.Join(root, "internal", "parser", "AGENTS.md"): "nearest",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := LoadAgents(AgentsOptions{UserConfigDir: user, ProjectRoot: root, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"user", "root", "internal", "nearest"}
	if len(loaded) != len(want) {
		t.Fatalf("loaded=%+v", loaded)
	}
	for index := range want {
		if loaded[index].Content != want[index] {
			t.Fatalf("loaded=%+v", loaded)
		}
	}
}

func TestLoadAgentsRejectsOversizedAndEscapedInput(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(strings.Repeat("x", maxAgentsFileBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgents(AgentsOptions{ProjectRoot: root, WorkDir: root}); err == nil {
		t.Fatal("oversized AGENTS.md was accepted")
	}
	if err := os.Remove(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgents(AgentsOptions{ProjectRoot: root, WorkDir: t.TempDir()}); err == nil {
		t.Fatal("outside working directory was accepted")
	}
}
