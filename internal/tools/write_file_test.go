// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFileCreatesNestedFileAfterCapability(t *testing.T) {
	env := newToolTestEnv(t)
	tool := NewWriteFile()
	call := prepareCall(t, tool, env, mustJSON(t, writeFileArgs{
		Path: "new/nested/file.txt", Content: "你好，MengDie\n",
	}))
	if call.Preview.Kind != PreviewDiff || !strings.Contains(call.Preview.Body, "--- /dev/null") || !strings.Contains(call.Preview.Body, "+你好，MengDie") {
		t.Fatalf("unexpected preview: %#v", call.Preview)
	}
	if len(call.Preconditions) != 1 || call.Preconditions[0].Kind != PreconditionPathAbsent {
		t.Fatalf("unexpected preconditions: %#v", call.Preconditions)
	}
	target := filepath.Join(env.root, "new", "nested", "file.txt")
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Prepare created target: %v", err)
	}

	result := executeCall(t, tool, env, call)
	if got := readTestFile(t, target); got != "你好，MengDie\n" {
		t.Fatalf("content = %q", got)
	}
	if result.Metadata["operation"] != "created" || result.Metadata["sha256"] != bytesSHA256([]byte("你好，MengDie\n")) || result.Metadata["size_bytes"] == "" {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
	}
	if len(env.journal.intents) != 1 {
		t.Fatalf("journal intents=%d", len(env.journal.intents))
	}
	for _, intent := range env.journal.intents {
		if intent.PreExists || !intent.PostExists || intent.Path != target || intent.PostSHA256 != result.Metadata["sha256"] {
			t.Fatalf("journal intent=%+v", intent)
		}
	}
	assertNoStagingFiles(t, env.root)
}

func TestWriteFileRequiresExplicitOverwriteAndPreservesMode(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "existing.txt", "before\n")
	path := filepath.Join(env.root, "existing.txt")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewWriteFile()
	withoutOverwrite := mustRawJSON(t, writeFileArgs{Path: "existing.txt", Content: "after\n"})
	if _, err := tool.Prepare(context.Background(), withoutOverwrite, env.prepareEnv()); err == nil {
		t.Fatal("Prepare() replaced existing file without overwrite=true")
	}
	call := prepareCall(t, tool, env, mustJSON(t, writeFileArgs{
		Path: "existing.txt", Content: "after\n", Overwrite: true,
	}))
	if !strings.Contains(call.Preview.Body, "-before") || !strings.Contains(call.Preview.Body, "+after") {
		t.Fatalf("unexpected preview: %q", call.Preview.Body)
	}
	result := executeCall(t, tool, env, call)
	if got := readTestFile(t, path); got != "after\n" {
		t.Fatalf("content = %q", got)
	}
	if result.Metadata["operation"] != "overwritten" {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("mode = %v, want 0600", got)
		}
	}
	assertNoStagingFiles(t, env.root)
}

func TestWriteFileRejectsNoopOverwriteBecauseJournalStatesWouldBeAmbiguous(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "same.txt", "same\n")
	tool := NewWriteFile()
	_, err := tool.Prepare(context.Background(), mustRawJSON(t, writeFileArgs{
		Path: "same.txt", Content: "same\n", Overwrite: true,
	}), env.prepareEnv())
	if err == nil || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("Prepare() error=%v", err)
	}
}

func TestWriteFileDetectsCreateTOCTOU(t *testing.T) {
	env := newToolTestEnv(t)
	tool := NewWriteFile()
	call := prepareCall(t, tool, env, mustJSON(t, writeFileArgs{
		Path: "new.txt", Content: "approved\n",
	}))
	target := filepath.Join(env.root, "new.txt")
	if err := os.WriteFile(target, []byte("appeared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var preconditionErr *PreconditionError
	if _, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv()); !errors.As(err, &preconditionErr) {
		t.Fatalf("Execute() error = %v, want PreconditionError", err)
	}
	if got := readTestFile(t, target); got != "appeared\n" {
		t.Fatalf("content = %q", got)
	}
	assertNoStagingFiles(t, env.root)
}

func TestWriteFileDetectsOverwriteTOCTOU(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "existing.txt", "before\n")
	tool := NewWriteFile()
	call := prepareCall(t, tool, env, mustJSON(t, writeFileArgs{
		Path: "existing.txt", Content: "approved\n", Overwrite: true,
	}))
	target := filepath.Join(env.root, "existing.txt")
	if err := os.WriteFile(target, []byte("changed elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var preconditionErr *PreconditionError
	if _, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv()); !errors.As(err, &preconditionErr) {
		t.Fatalf("Execute() error = %v, want PreconditionError", err)
	}
	if got := readTestFile(t, target); got != "changed elsewhere\n" {
		t.Fatalf("content = %q", got)
	}
	assertNoStagingFiles(t, env.root)
}

func TestWriteFileRejectsDeletedOrReplacedOverwriteTarget(t *testing.T) {
	for _, replacement := range []string{"deleted", "directory"} {
		t.Run(replacement, func(t *testing.T) {
			env := newToolTestEnv(t)
			env.write(t, "existing.txt", "before\n")
			tool := NewWriteFile()
			call := prepareCall(t, tool, env, mustJSON(t, writeFileArgs{
				Path: "existing.txt", Content: "after\n", Overwrite: true,
			}))
			target := filepath.Join(env.root, "existing.txt")
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if replacement == "directory" {
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			var preconditionErr *PreconditionError
			if _, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv()); !errors.As(err, &preconditionErr) {
				t.Fatalf("Execute() error = %v, want PreconditionError", err)
			}
			assertNoStagingFiles(t, env.root)
		})
	}
}

func TestWriteFileMissingCapabilityHasNoSideEffects(t *testing.T) {
	env := newToolTestEnv(t)
	tool := NewWriteFile()
	call := prepareCall(t, tool, env, mustJSON(t, writeFileArgs{
		Path: "missing/parents/file.txt", Content: "content\n",
	}))
	if _, err := tool.Execute(context.Background(), call, Capability{}, env.execEnv()); !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("Execute() error = %v, want ErrCapabilityMissing", err)
	}
	if _, err := os.Stat(filepath.Join(env.root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing capability created parent: %v", err)
	}
}

func TestWriteFileRequiresDurableJournalBeforeProjectMutation(t *testing.T) {
	env := newToolTestEnv(t)
	tool := NewWriteFile()
	call := prepareCall(t, tool, env, mustJSON(t, writeFileArgs{
		Path: "missing/parents/file.txt", Content: "content\n",
	}))
	execEnv := env.execEnv()
	execEnv.MutationJournal = nil
	if _, err := tool.Execute(context.Background(), call, capabilityFor(call), execEnv); !errors.Is(err, ErrMutationJournalMissing) {
		t.Fatalf("Execute() error=%v, want ErrMutationJournalMissing", err)
	}
	if _, err := os.Stat(filepath.Join(env.root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing journal created parent: %v", err)
	}
}

func TestWriteFileRechecksTOCTOUAfterJournalPrepare(t *testing.T) {
	env := newToolTestEnv(t)
	tool := NewWriteFile()
	call := prepareCall(t, tool, env, mustJSON(t, writeFileArgs{
		Path: "new.txt", Content: "approved\n",
	}))
	target := filepath.Join(env.root, "new.txt")
	execEnv := env.execEnv()
	execEnv.MutationJournal = interferingMutationJournal{path: target}
	var preconditionErr *PreconditionError
	if _, err := tool.Execute(context.Background(), call, capabilityFor(call), execEnv); !errors.As(err, &preconditionErr) {
		t.Fatalf("Execute() error=%v, want PreconditionError", err)
	}
	if got := readTestFile(t, target); got != "external\n" {
		t.Fatalf("content=%q", got)
	}
	assertNoStagingFiles(t, env.root)
}

func TestWriteFileRejectsUnsafeInputs(t *testing.T) {
	env := newToolTestEnv(t)
	tool := NewWriteFile()
	tests := []json.RawMessage{
		json.RawMessage(`{"path":"../outside.txt","content":"x"}`),
		json.RawMessage(`{"path":".env","content":"x"}`),
		json.RawMessage(`{"path":"file.txt","content":"x\u0000y"}`),
		json.RawMessage(`{"path":"file.txt","content":"x","unknown":true}`),
		mustRawJSON(t, writeFileArgs{Path: "large.txt", Content: strings.Repeat("x", maxWriteContentBytes+1)}),
	}
	for _, raw := range tests {
		if _, err := tool.Prepare(context.Background(), raw, env.prepareEnv()); err == nil {
			t.Errorf("Prepare(%s) succeeded, want error", raw)
		}
	}
	if _, err := tool.Prepare(context.Background(), mustRawJSON(t, writeFileArgs{
		Path: "missing.txt", Content: "x", Overwrite: true,
	}), env.prepareEnv()); err == nil {
		t.Fatal("Prepare() allowed overwrite=true for a missing target")
	}
}

func TestAtomicWriteFileCleansStagingAndCreatedParentsOnFailure(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("exists"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "new", "nested", "file.txt")
	err := atomicWriteFile(root, target, []byte("content"), 0o644, false, []Precondition{{
		Kind: PreconditionPathAbsent, Path: blocker,
	}}, nil)
	var preconditionErr *PreconditionError
	if !errors.As(err, &preconditionErr) {
		t.Fatalf("atomicWriteFile() error = %v, want PreconditionError", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created parents leaked: %v", err)
	}
	assertNoStagingFiles(t, root)
}

func TestAtomicWriteFileRootHandleRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	target := filepath.Join(link, "outside.txt")
	err := atomicWriteFile(root, target, []byte("must not escape"), 0o644, false, []Precondition{{
		Kind: PreconditionPathAbsent, Path: target,
	}}, nil)
	if err == nil {
		t.Fatal("atomicWriteFile() followed a symlink outside the project root")
	}
	if _, err := os.Stat(filepath.Join(outside, "outside.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside target was created: %v", err)
	}
	assertNoStagingFiles(t, root)
}

type interferingMutationJournal struct{ path string }

func (journal interferingMutationJournal) Prepare(context.Context, MutationIntent) (MutationReceipt, error) {
	if err := os.WriteFile(journal.path, []byte("external\n"), 0o644); err != nil {
		return MutationReceipt{}, err
	}
	return MutationReceipt{JournalID: "interfering"}, nil
}

func (interferingMutationJournal) MarkApplied(context.Context, MutationReceipt) error { return nil }

func (interferingMutationJournal) VerifyPost(context.Context, MutationReceipt) error { return nil }
