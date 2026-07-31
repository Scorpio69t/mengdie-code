// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileWithLineNumbersAndChinese(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "main.go", "package main\n\n// 梦蝶注释\nfunc main() {}\n")
	tool := NewReadFile()

	call := prepareCall(t, tool, env, `{"path":"main.go"}`)
	result := executeCall(t, tool, env, call)

	for _, want := range []string{"1\tpackage main", "3\t// 梦蝶注释", "4\tfunc main() {}"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output missing %q:\n%s", want, result.Output)
		}
	}
	if result.Metadata["encoding"] != "utf-8" || result.Metadata["sha256"] == "" {
		t.Errorf("metadata = %+v", result.Metadata)
	}
	if result.Truncated {
		t.Error("small file unexpectedly truncated")
	}
}

func TestReadFileLineRange(t *testing.T) {
	env := newToolTestEnv(t)
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, strings.Repeat("x", 3))
	}
	env.write(t, "ten.txt", strings.Join(lines, "\n")+"\n")
	tool := NewReadFile()

	call := prepareCall(t, tool, env, `{"path":"ten.txt","start_line":3,"end_line":5}`)
	result := executeCall(t, tool, env, call)

	if !strings.Contains(result.Output, "3\t") || !strings.Contains(result.Output, "5\t") {
		t.Fatalf("range output missing boundary lines:\n%s", result.Output)
	}
	if strings.Contains(result.Output, "6\t") || strings.Contains(result.Output, "2\t") {
		t.Fatalf("range output contains out-of-range lines:\n%s", result.Output)
	}
}

func TestReadFileTruncatesLargeFile(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "big.txt", strings.Repeat(strings.Repeat("y", 200)+"\n", 500))
	tool := NewReadFile()

	call := prepareCall(t, tool, env, `{"path":"big.txt"}`)
	result := executeCall(t, tool, env, call)

	if !result.Truncated || !strings.Contains(result.Output, "truncated") {
		t.Fatalf("large file not truncated:\n%.200s", result.Output)
	}
	if len(result.Output) > DefaultFileReadBytes+128 {
		t.Fatalf("output %d bytes exceeds budget", len(result.Output))
	}
}

func TestReadFileBinaryNotInjected(t *testing.T) {
	env := newToolTestEnv(t)
	if err := os.WriteFile(filepath.Join(env.root, "bin.dat"), []byte{'P', 'K', 0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadFile()

	call := prepareCall(t, tool, env, `{"path":"bin.dat"}`)
	result := executeCall(t, tool, env, call)

	if result.Metadata["encoding"] != "binary" {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
	if strings.Contains(result.Output, "PK") {
		t.Fatalf("binary content injected:\n%s", result.Output)
	}
}

func TestReadFileRejectsEscapeAndDirectory(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, filepath.Join("..", "outside.txt"), "secret")
	tool := NewReadFile()

	if _, err := tool.Prepare(context.Background(), json.RawMessage(`{"path":"../outside.txt"}`), env.prepareEnv()); err == nil {
		t.Fatal("Prepare() allowed escape")
	}
	if _, err := tool.Prepare(context.Background(), json.RawMessage(`{"path":"."}`), env.prepareEnv()); err == nil {
		t.Fatal("Prepare() allowed directory")
	}
	if _, err := tool.Prepare(context.Background(), json.RawMessage(`{"path":"main.go","start_line":5,"end_line":2}`), env.prepareEnv()); err == nil {
		t.Fatal("Prepare() allowed inverted range")
	}
	if _, err := tool.Prepare(context.Background(), json.RawMessage(`{"path":"main.go","bogus":1}`), env.prepareEnv()); err == nil {
		t.Fatal("Prepare() allowed unknown field")
	}
}

func TestReadFileSensitivePreview(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, ".env", "TOKEN=x")
	tool := NewReadFile()

	call := prepareCall(t, tool, env, `{"path":".env"}`)
	if !strings.Contains(call.Preview.Title, "敏感") {
		t.Fatalf("sensitive file not flagged in preview: %+v", call.Preview)
	}
}

func TestReadFileCapabilityAndTOCTOU(t *testing.T) {
	env := newToolTestEnv(t)
	path := filepath.Join(env.root, "file.txt")
	env.write(t, "file.txt", "before")
	tool := NewReadFile()
	call := prepareCall(t, tool, env, `{"path":"file.txt"}`)

	if _, err := tool.Execute(context.Background(), call, Capability{}, env.execEnv()); !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("Execute() error = %v, want ErrCapabilityMissing", err)
	}
	bad := capabilityFor(call)
	bad.Digest = ComputeDigest("read_file", json.RawMessage(`{"path":"other.txt"}`))
	if _, err := tool.Execute(context.Background(), call, bad, env.execEnv()); !errors.Is(err, ErrCapabilityMismatch) {
		t.Fatalf("Execute() error = %v, want ErrCapabilityMismatch", err)
	}

	// Content changes after Prepare: precondition must fail safely.
	if err := os.WriteFile(path, []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	var preconditionErr *PreconditionError
	if _, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv()); !errors.As(err, &preconditionErr) {
		t.Fatalf("Execute() error = %v, want PreconditionError", err)
	}
}

func TestReadFileLongLineTruncated(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "long.txt", strings.Repeat("z", 2000)+"\nshort\n")
	tool := NewReadFile()

	call := prepareCall(t, tool, env, `{"path":"long.txt"}`)
	result := executeCall(t, tool, env, call)
	for _, line := range strings.Split(result.Output, "\n") {
		if len([]rune(line)) > MaxMatchLineLength+16 {
			t.Fatalf("line exceeds rune budget: %.80s…", line)
		}
	}
}
