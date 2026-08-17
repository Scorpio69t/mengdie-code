// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func TestDiscoverUsesProjectSkillOnConflictAndSortsCatalog(t *testing.T) {
	project := t.TempDir()
	user := t.TempDir()
	writeSkill(t, filepath.Join(user, ".mengdie", "skills"), "shared", "user description", "user body")
	writeSkill(t, filepath.Join(user, ".mengdie", "skills"), "alpha", "alpha description", "alpha body")
	writeSkill(t, filepath.Join(project, ".mengdie", "skills"), "shared", "project description", "project body")

	catalog, err := Discover(Options{UserHomeDir: user, ProjectRoot: project})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 2 || catalog.Skills[0].Name != "alpha" || catalog.Skills[1].Name != "shared" {
		t.Fatalf("skills=%+v", catalog.Skills)
	}
	shared := catalog.Skills[1]
	if shared.Scope != ScopeProject || shared.Description != "project description" || !strings.HasPrefix(shared.Source, "$PROJECT_ROOT/") {
		t.Fatalf("shared=%+v", shared)
	}
	if len(catalog.Conflicts) != 1 || catalog.Conflicts[0].Name != "shared" ||
		!strings.HasPrefix(catalog.Conflicts[0].IgnoredSource, "~/.mengdie/") {
		t.Fatalf("conflicts=%+v", catalog.Conflicts)
	}
}

func TestDiscoverRejectsInvalidMetadataAndOversizedFiles(t *testing.T) {
	for name, content := range map[string]string{
		"mismatched name":     "---\nname: other\ndescription: useful\n---\nbody",
		"missing description": "---\nname: test\n---\nbody",
		"oversized":           "---\nname: test\ndescription: useful\n---\n" + strings.Repeat("x", maxSkillFileBytes),
	} {
		t.Run(name, func(t *testing.T) {
			project := t.TempDir()
			path := filepath.Join(project, ".mengdie", "skills", "test", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Discover(Options{ProjectRoot: project}); err == nil {
				t.Fatal("Discover() accepted invalid SKILL.md")
			}
		})
	}
}

func TestReadSkillReturnsSnapshotAndRejectsChangedFile(t *testing.T) {
	project := t.TempDir()
	writeSkill(t, filepath.Join(project, ".mengdie", "skills"), "review", "review changes", "original body")
	catalog, err := Discover(Options{ProjectRoot: project})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewReadTool(catalog)
	if err != nil {
		t.Fatal(err)
	}
	call, err := tool.Prepare(context.Background(), json.RawMessage(`{"name":"review"}`), tools.PrepareEnv{CallID: "call-1"})
	if err != nil {
		t.Fatal(err)
	}
	guard, err := platform.NewPathGuard(project)
	if err != nil {
		t.Fatal(err)
	}
	capability := tools.Capability{Nonce: "nonce", ToolName: call.ToolName, Digest: call.Digest}
	result, err := tool.Execute(context.Background(), call, capability, tools.ExecEnv{
		RunID: "run-1", Guard: guard, CapabilityVerifier: allowCapability{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "original body") || !strings.Contains(result.Output, "不授予额外权限") {
		t.Fatalf("output=%q", result.Output)
	}

	writeSkill(t, filepath.Join(project, ".mengdie", "skills"), "review", "review changes", "changed body")
	capability.Nonce = "new-nonce"
	if _, err := tool.Execute(context.Background(), call, capability, tools.ExecEnv{
		RunID: "run-1", Guard: guard, CapabilityVerifier: allowCapability{},
	}); err == nil || !strings.Contains(err.Error(), "changed after discovery") {
		t.Fatalf("Execute() error=%v", err)
	}
}

func TestReadSkillRejectsUnknownAndTrailingArguments(t *testing.T) {
	project := t.TempDir()
	writeSkill(t, filepath.Join(project, ".mengdie", "skills"), "review", "review changes", "body")
	catalog, err := Discover(Options{ProjectRoot: project})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewReadTool(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"name":"missing"}`, `{"name":"review","path":"x"}`, `{"name":"review"}{}`} {
		if _, err := tool.Prepare(context.Background(), json.RawMessage(raw), tools.PrepareEnv{CallID: "call"}); err == nil {
			t.Fatalf("Prepare(%s) succeeded", raw)
		}
	}
}

func writeSkill(t *testing.T, root, name, description, body string) {
	t.Helper()
	path := filepath.Join(root, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type allowCapability struct{}

func (allowCapability) Consume(context.Context, *tools.PreparedCall, tools.Capability, tools.CapabilityUse) error {
	return nil
}

var _ tools.CapabilityVerifier = allowCapability{}
