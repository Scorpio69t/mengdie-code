// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

// toolTestEnv wires a temp project root with guard and environments.
type toolTestEnv struct {
	root  string
	guard *platform.PathGuard
}

func newToolTestEnv(t *testing.T) *toolTestEnv {
	t.Helper()
	root := t.TempDir()
	guard, err := platform.NewPathGuard(root)
	if err != nil {
		t.Fatalf("NewPathGuard() error = %v", err)
	}
	return &toolTestEnv{root: root, guard: guard}
}

func (e *toolTestEnv) write(t *testing.T, rel, content string) {
	t.Helper()
	path := filepath.Join(e.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *toolTestEnv) prepareEnv() PrepareEnv {
	return PrepareEnv{CallID: "call-1", Guard: e.guard}
}

func (e *toolTestEnv) execEnv() ExecEnv {
	return ExecEnv{Guard: e.guard}
}

func prepareCall(t *testing.T, tool Tool, env *toolTestEnv, raw string) *PreparedCall {
	t.Helper()
	call, err := tool.Prepare(context.Background(), json.RawMessage(raw), env.prepareEnv())
	if err != nil {
		t.Fatalf("Prepare(%s) error = %v", raw, err)
	}
	return call
}

func capabilityFor(call *PreparedCall) Capability {
	return Capability{
		RunID:    "run-1",
		ToolName: call.ToolName,
		Digest:   call.Digest,
		Nonce:    "nonce-1",
	}
}

func executeCall(t *testing.T, tool Tool, env *toolTestEnv, call *PreparedCall) *ToolResult {
	t.Helper()
	result, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	return result
}
