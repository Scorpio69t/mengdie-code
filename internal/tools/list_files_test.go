// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestListFilesIgnoreRulesAndSorting(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "b.txt", "")
	env.write(t, "a.go", "")
	env.write(t, "sub/c.go", "")
	env.write(t, "sub/deep/d.go", "")
	env.write(t, ".git/config", "")
	env.write(t, ".hidden/file.txt", "")
	env.write(t, ".hiddenfile", "")
	env.write(t, "node_modules/pkg/index.js", "")
	env.write(t, "build/output.bin", "")
	tool := NewListFiles()

	call := prepareCall(t, tool, env, `{}`)
	result := executeCall(t, tool, env, call)

	got := strings.Split(result.Output, "\n")
	want := []string{"a.go", "b.txt", "sub/c.go", "sub/deep/d.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for _, banned := range []string{".git", "node_modules", "build", ".hidden"} {
		if strings.Contains(result.Output, banned) {
			t.Errorf("output contains ignored entry %q:\n%s", banned, result.Output)
		}
	}
}

func TestListFilesDepthGlobAndUnicode(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "top.go", "")
	env.write(t, "sub/中间文件.go", "")
	env.write(t, "sub/deep/nested.go", "")
	env.write(t, "sub/note.md", "")
	tool := NewListFiles()

	call := prepareCall(t, tool, env, `{"max_depth":1}`)
	result := executeCall(t, tool, env, call)
	if result.Output != "top.go" {
		t.Fatalf("max_depth=1 output = %q", result.Output)
	}

	call = prepareCall(t, tool, env, `{"glob":"*.go"}`)
	result = executeCall(t, tool, env, call)
	for _, want := range []string{"top.go", "sub/中间文件.go", "sub/deep/nested.go"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("glob output missing %q:\n%s", want, result.Output)
		}
	}
	if strings.Contains(result.Output, "note.md") {
		t.Errorf("glob output contains non-matching file:\n%s", result.Output)
	}

	call = prepareCall(t, tool, env, `{"path":"sub","glob":"deep/*.go"}`)
	result = executeCall(t, tool, env, call)
	if result.Output != "sub/deep/nested.go" {
		t.Fatalf("slash glob output = %q", result.Output)
	}
}

func TestListFilesLimitTruncation(t *testing.T) {
	env := newToolTestEnv(t)
	for i := 0; i < 120; i++ {
		env.write(t, fmt.Sprintf("many/f%03d.txt", i), "")
	}
	tool := NewListFiles()

	call := prepareCall(t, tool, env, `{"limit":50}`)
	result := executeCall(t, tool, env, call)

	if !result.Truncated || !strings.Contains(result.Output, "truncated: limit 50") {
		t.Fatalf("limit truncation not reported:\n%.200s", result.Output)
	}
	entries := strings.Split(strings.TrimRight(strings.Split(result.Output, "…")[0], "\n"), "\n")
	if got := len(entries); got != 50 {
		t.Fatalf("entries = %d, want 50", got)
	}
}

func TestListFilesRejectsEscapeAndFile(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "file.txt", "")
	tool := NewListFiles()

	if _, err := tool.Prepare(context.Background(), json.RawMessage(`{"path":".."}`), env.prepareEnv()); err == nil {
		t.Fatal("Prepare() allowed escape")
	}
	if _, err := tool.Prepare(context.Background(), json.RawMessage(`{"path":"file.txt"}`), env.prepareEnv()); err == nil {
		t.Fatal("Prepare() allowed file as directory")
	}
	if _, err := tool.Prepare(context.Background(), json.RawMessage(`{"glob":"[bad"}`), env.prepareEnv()); err == nil {
		t.Fatal("Prepare() allowed invalid glob")
	}
}

func TestListFilesCapability(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "a.go", "")
	tool := NewListFiles()
	call := prepareCall(t, tool, env, `{}`)

	if _, err := tool.Execute(context.Background(), call, Capability{}, env.execEnv()); err == nil {
		t.Fatal("Execute() without capability succeeded")
	}
}
