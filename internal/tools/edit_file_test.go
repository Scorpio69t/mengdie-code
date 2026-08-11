// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEditFilePrepareAndExecute(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "main.go", "package main\n\nfunc oldName() {}\n")
	path := filepath.Join(env.root, "main.go")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tool := NewEditFile()
	call := prepareCall(t, tool, env, mustJSON(t, editFileArgs{
		Path:    "main.go",
		OldText: "oldName",
		NewText: "newName",
	}))
	if call.Preview.Kind != PreviewDiff || !strings.Contains(call.Preview.Body, "-oldName") || !strings.Contains(call.Preview.Body, "+newName") {
		t.Fatalf("unexpected preview: %#v", call.Preview)
	}
	if len(call.Preconditions) != 1 || call.Preconditions[0].Kind != PreconditionFileSHA256 {
		t.Fatalf("unexpected preconditions: %#v", call.Preconditions)
	}

	result := executeCall(t, tool, env, call)
	wantContent := "package main\n\nfunc newName() {}\n"
	if got := readTestFile(t, path); got != wantContent {
		t.Fatalf("content = %q", got)
	}
	if result.Metadata["sha256_before"] == "" || result.Metadata["sha256_after"] != bytesSHA256([]byte(wantContent)) || result.Metadata["replacements"] != "1" {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
	}
	if len(env.journal.intents) != 1 {
		t.Fatalf("journal intents=%d", len(env.journal.intents))
	}
	for _, intent := range env.journal.intents {
		if !intent.PreExists || !intent.PostExists || intent.PreSHA256 == intent.PostSHA256 || intent.Path != path {
			t.Fatalf("journal intent=%+v", intent)
		}
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

func TestEditFileRequiresExactReplacementCount(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "repeat.txt", "same same\n")
	tool := NewEditFile()

	if _, err := tool.Prepare(context.Background(), mustRawJSON(t, editFileArgs{
		Path: "repeat.txt", OldText: "same", NewText: "changed",
	}), env.prepareEnv()); err == nil || !strings.Contains(err.Error(), "matched 2 times, expected 1") {
		t.Fatalf("Prepare() error = %v", err)
	}
	if got := readTestFile(t, filepath.Join(env.root, "repeat.txt")); got != "same same\n" {
		t.Fatalf("Prepare changed content: %q", got)
	}
	call := prepareCall(t, tool, env, mustJSON(t, editFileArgs{
		Path: "repeat.txt", OldText: "same", NewText: "changed", ExpectedReplacements: 2,
	}))
	executeCall(t, tool, env, call)
	if got := readTestFile(t, filepath.Join(env.root, "repeat.txt")); got != "changed changed\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestEditFileRejectsDeletedOrReplacedTarget(t *testing.T) {
	for _, replacement := range []string{"deleted", "directory"} {
		t.Run(replacement, func(t *testing.T) {
			env := newToolTestEnv(t)
			env.write(t, "file.txt", "before\n")
			path := filepath.Join(env.root, "file.txt")
			tool := NewEditFile()
			call := prepareCall(t, tool, env, mustJSON(t, editFileArgs{
				Path: "file.txt", OldText: "before", NewText: "after",
			}))
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if replacement == "directory" {
				if err := os.Mkdir(path, 0o755); err != nil {
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

func TestEditFileCapabilityAndTOCTOU(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "file.txt", "before\n")
	path := filepath.Join(env.root, "file.txt")
	tool := NewEditFile()
	call := prepareCall(t, tool, env, mustJSON(t, editFileArgs{
		Path: "file.txt", OldText: "before", NewText: "after",
	}))

	if _, err := tool.Execute(context.Background(), call, Capability{}, env.execEnv()); !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("Execute() error = %v, want ErrCapabilityMissing", err)
	}
	if got := readTestFile(t, path); got != "before\n" {
		t.Fatalf("content changed without capability: %q", got)
	}
	if err := os.WriteFile(path, []byte("changed elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var preconditionErr *PreconditionError
	if _, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv()); !errors.As(err, &preconditionErr) {
		t.Fatalf("Execute() error = %v, want PreconditionError", err)
	}
	if got := readTestFile(t, path); got != "changed elsewhere\n" {
		t.Fatalf("TOCTOU failure changed content: %q", got)
	}
	assertNoStagingFiles(t, env.root)
}

func TestEditFileRejectsResolvedPathChange(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "first.txt", "first\n")
	env.write(t, "second.txt", "second\n")
	link := filepath.Join(env.root, "alias.txt")
	if err := os.Symlink(filepath.Join(env.root, "first.txt"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	tool := NewEditFile()
	call := prepareCall(t, tool, env, mustJSON(t, editFileArgs{
		Path: "alias.txt", OldText: "first", NewText: "edited",
	}))
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(env.root, "second.txt"), link); err != nil {
		t.Fatal(err)
	}
	var preconditionErr *PreconditionError
	if _, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv()); !errors.As(err, &preconditionErr) {
		t.Fatalf("Execute() error = %v, want PreconditionError", err)
	}
	if got := readTestFile(t, filepath.Join(env.root, "first.txt")); got != "first\n" {
		t.Fatalf("first content = %q", got)
	}
	if got := readTestFile(t, filepath.Join(env.root, "second.txt")); got != "second\n" {
		t.Fatalf("second content = %q", got)
	}
}

func TestEditFileRejectsUnsafeInputs(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "file.txt", "text\n")
	if err := os.WriteFile(filepath.Join(env.root, "binary.bin"), []byte{'x', 0, 'y'}, 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFile()
	tests := []string{
		`{"path":"file.txt","old_text":"","new_text":"x"}`,
		`{"path":"file.txt","old_text":"text","new_text":"text"}`,
		`{"path":"file.txt","old_text":"text","new_text":"x","expected_replacements":1001}`,
		`{"path":"file.txt","old_text":"text","new_text":"x","unknown":true}`,
		`{"path":"../outside.txt","old_text":"x","new_text":"y"}`,
		`{"path":".env","old_text":"x","new_text":"y"}`,
		`{"path":"binary.bin","old_text":"x","new_text":"y"}`,
	}
	for _, raw := range tests {
		if _, err := tool.Prepare(context.Background(), json.RawMessage(raw), env.prepareEnv()); err == nil {
			t.Errorf("Prepare(%s) succeeded, want error", raw)
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	return string(mustRawJSON(t, value))
}

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func assertNoStagingFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".mengdie-write-") {
			t.Errorf("staging file leaked: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
