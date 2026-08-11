// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

// toolTestEnv wires a temp project root with guard and environments.
type toolTestEnv struct {
	root    string
	guard   *platform.PathGuard
	journal *testMutationJournal
}

func newToolTestEnv(t *testing.T) *toolTestEnv {
	t.Helper()
	guard, err := platform.NewPathGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewPathGuard() error = %v", err)
	}
	return &toolTestEnv{root: guard.Root(), guard: guard, journal: &testMutationJournal{intents: make(map[string]MutationIntent)}}
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
	return ExecEnv{
		RunID: "run-1", Guard: e.guard, CapabilityVerifier: testCapabilityVerifier{},
		MutationJournal: e.journal,
	}
}

type testMutationJournal struct {
	intents map[string]MutationIntent
	next    int
}

func (journal *testMutationJournal) Prepare(_ context.Context, intent MutationIntent) (MutationReceipt, error) {
	journal.next++
	id := fmt.Sprintf("test-journal-%d", journal.next)
	journal.intents[id] = intent
	return MutationReceipt{JournalID: id}, nil
}

func (*testMutationJournal) MarkApplied(context.Context, MutationReceipt) error { return nil }

func (journal *testMutationJournal) VerifyPost(_ context.Context, receipt MutationReceipt) error {
	intent, ok := journal.intents[receipt.JournalID]
	if !ok {
		return errors.New("test mutation journal receipt not found")
	}
	digest, err := FileSHA256(intent.Path)
	if err != nil {
		return err
	}
	if digest != intent.PostSHA256 {
		return ErrMutationConflict
	}
	return nil
}

type testCapabilityVerifier struct{}

func (testCapabilityVerifier) Consume(_ context.Context, call *PreparedCall, cap Capability, use CapabilityUse) error {
	if cap.RunID != use.RunID || cap.ToolName != call.ToolName || cap.Digest != call.Digest {
		return ErrCapabilityMismatch
	}
	return nil
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
