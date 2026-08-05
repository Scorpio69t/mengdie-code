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
	"time"
)

func TestCanonicalizeStabilizesKeyOrderAndWhitespace(t *testing.T) {
	a, err := Canonicalize(json.RawMessage(`{ "b": 1, "a": {"y": [2, 3], "x": "s"} }`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Canonicalize(json.RawMessage(`{"a":{"x":"s","y":[2,3]},"b":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("canonical forms differ: %s vs %s", a, b)
	}
	if ComputeDigest("tool", a) != ComputeDigest("tool", b) {
		t.Fatal("digests differ for semantically identical arguments")
	}
}

func TestCanonicalizeRejectsInvalidJSON(t *testing.T) {
	for _, raw := range []string{``, `{`, `{"a":}`, `nul`} {
		if _, err := Canonicalize(json.RawMessage(raw)); err == nil {
			t.Fatalf("Canonicalize(%q) succeeded", raw)
		}
	}
}

func TestComputeDigestBindsToolName(t *testing.T) {
	arg := json.RawMessage(`{"path":"a.txt"}`)
	if ComputeDigest("read_file", arg) == ComputeDigest("write_file", arg) {
		t.Fatal("digest does not bind tool name")
	}
	if ComputeDigest("read_file", arg) == ComputeDigest("read_file", json.RawMessage(`{"path":"b.txt"}`)) {
		t.Fatal("digest does not bind arguments")
	}
}

func TestPrepareCallProducesValidCall(t *testing.T) {
	call, err := PrepareCall("id-1", "read_file", json.RawMessage(`{"path":"a.txt"}`), []Effect{EffectRead}, nil, Preview{Kind: PreviewRead}, nil)
	if err != nil {
		t.Fatalf("PrepareCall() error = %v", err)
	}
	if call.Digest == "" || string(call.CanonicalArg) != `{"path":"a.txt"}` {
		t.Fatalf("call = %+v", call)
	}
}

func TestPreparedCallValidate(t *testing.T) {
	base := func() *PreparedCall {
		arg := json.RawMessage(`{}`)
		return &PreparedCall{
			ID:           "id",
			ToolName:     "tool",
			CanonicalArg: arg,
			Effects:      []Effect{EffectRead},
			Digest:       ComputeDigest("tool", arg),
		}
	}

	t.Run("valid", func(t *testing.T) {
		if err := base().Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	for name, mutate := range map[string]func(*PreparedCall){
		"empty id":         func(c *PreparedCall) { c.ID = "" },
		"empty tool":       func(c *PreparedCall) { c.ToolName = "" },
		"invalid json":     func(c *PreparedCall) { c.CanonicalArg = json.RawMessage(`{`) },
		"no effects":       func(c *PreparedCall) { c.Effects = nil },
		"unknown effect":   func(c *PreparedCall) { c.Effects = []Effect{"fly"} },
		"duplicate effect": func(c *PreparedCall) { c.Effects = []Effect{EffectRead, EffectRead} },
		"empty digest":     func(c *PreparedCall) { c.Digest = "" },
		"digest mismatch":  func(c *PreparedCall) { c.Digest = ComputeDigest("other_tool", c.CanonicalArg) },
		"bad precondition": func(c *PreparedCall) { c.Preconditions = []Precondition{{Kind: "magic"}} },
		"hashless precondition": func(c *PreparedCall) {
			c.Preconditions = []Precondition{{Kind: PreconditionFileSHA256, Path: "a.txt"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			call := base()
			mutate(call)
			if err := call.Validate(); err == nil {
				t.Fatal("Validate() succeeded, want error")
			}
		})
	}
}

func TestPreparedCallValidateRejectsUnsafeResourcesAndOversizedPreview(t *testing.T) {
	root := t.TempDir()
	call, err := PrepareCall("id", "tool", json.RawMessage(`{}`), []Effect{EffectRead},
		[]PathResource{{Path: filepath.Join(root, "file.txt")}}, Preview{Kind: PreviewRead}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PreparedCall){
		"relative path": func(c *PreparedCall) { c.Paths[0].Path = "file.txt" },
		"unclean path": func(c *PreparedCall) {
			c.Paths[0].Path = filepath.Join(root, "sub", "..", "file.txt") + string(filepath.Separator) + ".."
		},
		"duplicate path": func(c *PreparedCall) { c.Paths = append(c.Paths, c.Paths[0]) },
		"large title":    func(c *PreparedCall) { c.Preview.Title = strings.Repeat("x", 4097) },
		"large body":     func(c *PreparedCall) { c.Preview.Body = strings.Repeat("x", DefaultToolOutputBytes+1) },
	} {
		t.Run(name, func(t *testing.T) {
			copyCall := *call
			copyCall.Paths = append([]PathResource(nil), call.Paths...)
			mutate(&copyCall)
			if err := copyCall.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
}

func TestCheckCapabilityRequiresVerifier(t *testing.T) {
	call, err := PrepareCall("id", "tool", json.RawMessage(`{}`), []Effect{EffectRead}, nil, Preview{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	capability := Capability{ToolName: call.ToolName, Digest: call.Digest, Nonce: "not-authority"}
	err = CheckCapability(context.Background(), call, capability, ExecEnv{Now: func() time.Time { return time.Now() }})
	if !errors.Is(err, ErrCapabilityVerifierMissing) {
		t.Fatalf("CheckCapability() error = %v", err)
	}
}

func TestCheckPreconditionsDetectsTOCTOU(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	preconditions := []Precondition{{Kind: PreconditionFileSHA256, Path: path, SHA256: hash}}

	if err := CheckPreconditions(preconditions); err != nil {
		t.Fatalf("CheckPreconditions() error = %v", err)
	}

	if err := os.WriteFile(path, []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = CheckPreconditions(preconditions)
	var preconditionErr *PreconditionError
	if !errors.As(err, &preconditionErr) || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("CheckPreconditions() error = %v, want content-changed PreconditionError", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := CheckPreconditions(preconditions); !errors.As(err, &preconditionErr) {
		t.Fatalf("CheckPreconditions() error = %v, want missing-file PreconditionError", err)
	}
}

type stubTool struct {
	spec ToolSpec
}

func (s stubTool) Spec() ToolSpec { return s.spec }

func (s stubTool) Prepare(_ context.Context, _ json.RawMessage, _ PrepareEnv) (*PreparedCall, error) {
	return nil, nil
}

func (s stubTool) Execute(_ context.Context, _ *PreparedCall, _ Capability, _ ExecEnv) (*ToolResult, error) {
	return nil, nil
}

func TestRegistry(t *testing.T) {
	valid := func(name string) stubTool {
		return stubTool{spec: ToolSpec{Name: name, Effects: []Effect{EffectRead}, InputSchema: json.RawMessage(`{"type":"object"}`)}}
	}

	registry, err := NewRegistry(valid("b_tool"), valid("a_tool"))
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	if _, ok := registry.Lookup("a_tool"); !ok {
		t.Fatal("Lookup() missing registered tool")
	}
	if _, ok := registry.Lookup("missing"); ok {
		t.Fatal("Lookup() found unregistered tool")
	}
	specs := registry.Specs()
	if len(specs) != 2 || specs[0].Name != "a_tool" || specs[1].Name != "b_tool" {
		t.Fatalf("Specs() = %+v, want sorted by name", specs)
	}

	for name, tools := range map[string][]Tool{
		"duplicate name": {valid("x"), valid("x")},
		"empty name":     {stubTool{spec: ToolSpec{Effects: []Effect{EffectRead}}}},
		"no effects":     {stubTool{spec: ToolSpec{Name: "x"}}},
		"invalid schema": {stubTool{spec: ToolSpec{Name: "x", Effects: []Effect{EffectRead}, InputSchema: json.RawMessage(`{`)}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRegistry(tools...); err == nil {
				t.Fatal("NewRegistry() succeeded, want error")
			}
		})
	}
}
