// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func newSearchTool(forceFallback bool) Tool {
	return searchTextTool{forceFallback: forceFallback}
}

func searchEngines(t *testing.T) map[string]Tool {
	t.Helper()
	engines := map[string]Tool{"fallback": newSearchTool(true)}
	if _, err := exec.LookPath("rg"); err == nil {
		engines["rg"] = newSearchTool(false)
	} else {
		t.Log("rg not available, skipping rg engine")
	}
	return engines
}

func TestSearchTextBothEngines(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "a.go", "package a\n\n// 梦蝶 dream 注释\nvar X = 1\n")
	env.write(t, "sub/b.go", "package b\nvar Dream = 2\nvar dream2 = 3\n")
	env.write(t, "sub/c.txt", "nothing here\n")
	env.write(t, "node_modules/lib/dep.js", "dream in dependency\n")

	outputs := map[string]string{}
	for name, tool := range searchEngines(t) {
		call := prepareCall(t, tool, env, `{"query":"dream"}`)
		result := executeCall(t, tool, env, call)
		if result.Metadata["engine"] == "" {
			t.Fatalf("%s: missing engine metadata", name)
		}
		for _, want := range []string{"a.go:3: // 梦蝶 dream 注释", "sub/b.go:3: var dream2 = 3"} {
			if !strings.Contains(result.Output, want) {
				t.Errorf("%s: output missing %q:\n%s", name, want, result.Output)
			}
		}
		// Default is case-sensitive: "Dream" must not match.
		if strings.Contains(result.Output, "var Dream = 2") {
			t.Errorf("%s: case-sensitive default matched 'Dream':\n%s", name, result.Output)
		}
		if strings.Contains(result.Output, "node_modules") {
			t.Errorf("%s: searched ignored directory:\n%s", name, result.Output)
		}
		outputs[name] = result.Output
	}
	if len(outputs) == 2 && outputs["rg"] != outputs["fallback"] {
		t.Errorf("engines disagree:\nrg:\n%s\nfallback:\n%s", outputs["rg"], outputs["fallback"])
	}
}

func TestSearchTextCaseInsensitive(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "a.go", "var Dream = 1\n")

	for name, tool := range searchEngines(t) {
		call := prepareCall(t, tool, env, `{"query":"dream","case_sensitive":false}`)
		result := executeCall(t, tool, env, call)
		if !strings.Contains(result.Output, "a.go:1: var Dream = 1") {
			t.Errorf("%s: insensitive search missed match:\n%s", name, result.Output)
		}
	}
}

func TestSearchTextGlobAndLimit(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "x.go", "needle\n")
	env.write(t, "x.md", "needle\n")
	var many []string
	for i := 0; i < 60; i++ {
		many = append(many, "needle line")
	}
	env.write(t, "many.go", strings.Join(many, "\n")+"\n")

	for name, tool := range searchEngines(t) {
		call := prepareCall(t, tool, env, `{"query":"needle","glob":"*.go","limit":10}`)
		result := executeCall(t, tool, env, call)
		if strings.Contains(result.Output, "x.md") {
			t.Errorf("%s: glob filter ignored:\n%.200s", name, result.Output)
		}
		if !result.Truncated || !strings.Contains(result.Output, "limit 10") {
			t.Errorf("%s: limit truncation not reported:\n%.200s", name, result.Output)
		}
	}
}

func TestSearchTextLongLineAndBinary(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "long.txt", strings.Repeat("a", 800)+" needle "+strings.Repeat("b", 800)+"\n")
	env.write(t, "bin.dat", string([]byte{'n', 'e', 'e', 'd', 'l', 'e', 0, 1})+"\n")
	tool := newSearchTool(true)

	call := prepareCall(t, tool, env, `{"query":"needle"}`)
	result := executeCall(t, tool, env, call)

	if !strings.Contains(result.Output, "long.txt:1:") {
		t.Fatalf("long line match missing:\n%.120s", result.Output)
	}
	for _, line := range strings.Split(result.Output, "\n") {
		if len([]rune(line)) > MaxMatchLineLength+32 {
			t.Fatalf("match line exceeds budget: %d runes", len([]rune(line)))
		}
	}
	if strings.Contains(result.Output, "bin.dat") {
		t.Fatalf("binary file searched:\n%s", result.Output)
	}
}

func TestSearchTextResultSorted(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "z.go", "needle\n")
	env.write(t, "a.go", "needle\nneedle\n")
	tool := newSearchTool(true)

	call := prepareCall(t, tool, env, `{"query":"needle"}`)
	result := executeCall(t, tool, env, call)

	want := "a.go:1: needle\na.go:2: needle\nz.go:1: needle"
	if result.Output != want {
		t.Fatalf("output = %q, want %q", result.Output, want)
	}
}

func TestSearchTextRejectsBadInput(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "a.go", "")
	tool := NewSearchText()

	for _, raw := range []string{
		`{"query":""}`,
		`{"query":"x","path":".."}`,
		`{"query":"x","limit":9999}`,
		`{"query":"x","unknown":1}`,
	} {
		if _, err := tool.Prepare(context.Background(), json.RawMessage(raw), env.prepareEnv()); err == nil {
			t.Errorf("Prepare(%s) succeeded, want error", raw)
		}
	}

	call := prepareCall(t, tool, env, `{"query":"x"}`)
	if _, err := tool.Execute(context.Background(), call, Capability{}, env.execEnv()); err == nil {
		t.Fatal("Execute() without capability succeeded")
	}
}

func TestSearchTextNoMatch(t *testing.T) {
	env := newToolTestEnv(t)
	env.write(t, "a.go", "package a\n")
	for name, tool := range searchEngines(t) {
		call := prepareCall(t, tool, env, `{"query":"absent"}`)
		result := executeCall(t, tool, env, call)
		if result.Truncated || !strings.Contains(result.Output, "没有匹配") {
			t.Errorf("%s: no-match output = %q truncated=%v", name, result.Output, result.Truncated)
		}
	}
}
