// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func boolPointer(value bool) *bool { return &value }

func testRoot(t *testing.T) string {
	t.Helper()
	guard, err := platform.NewPathGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewPathGuard() error = %v", err)
	}
	return guard.Root()
}

func testCall(t *testing.T, root string, effects []tools.Effect, relative string, sensitive bool) *tools.PreparedCall {
	t.Helper()
	var paths []tools.PathResource
	if relative != "" {
		path := relative
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, relative)
		}
		paths = []tools.PathResource{{Path: filepath.Clean(path), Sensitive: sensitive}}
	}
	call, err := tools.PrepareCall(
		"call-1", "test_tool", json.RawMessage(`{"value":1}`), effects, paths,
		tools.Preview{Kind: tools.PreviewRead, Title: "测试调用"}, nil,
	)
	if err != nil {
		t.Fatalf("PrepareCall() error = %v", err)
	}
	return call
}

func testEngine(t *testing.T, root string, mode Mode, mutate func(*Options)) *Engine {
	t.Helper()
	options := Options{Root: root, Mode: mode}
	if mutate != nil {
		mutate(&options)
	}
	engine, err := NewEngine(options)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func TestEngineDefaultMatrix(t *testing.T) {
	root := testRoot(t)
	tests := []struct {
		name      string
		mode      Mode
		effects   []tools.Effect
		sensitive bool
		want      Decision
	}{
		{"interactive read", ModeInteractive, []tools.Effect{tools.EffectRead}, false, DecisionAllow},
		{"interactive sensitive read", ModeInteractive, []tools.Effect{tools.EffectRead}, true, DecisionAsk},
		{"interactive write", ModeInteractive, []tools.Effect{tools.EffectWrite}, false, DecisionAsk},
		{"interactive execute", ModeInteractive, []tools.Effect{tools.EffectExecute}, false, DecisionAsk},
		{"headless read", ModeHeadless, []tools.Effect{tools.EffectRead}, false, DecisionAllow},
		{"headless sensitive read", ModeHeadless, []tools.Effect{tools.EffectRead}, true, DecisionDeny},
		{"headless write", ModeHeadless, []tools.Effect{tools.EffectWrite}, false, DecisionDeny},
		{"headless execute", ModeHeadless, []tools.Effect{tools.EffectExecute}, false, DecisionDeny},
		{"headless run state", ModeHeadless, []tools.Effect{tools.EffectState}, false, DecisionAllow},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := testEngine(t, root, test.mode, nil)
			got := engine.Evaluate(testCall(t, root, test.effects, "file.txt", test.sensitive))
			if got.Decision != test.want {
				t.Fatalf("decision = %q (%s), want %q", got.Decision, got.Rule, test.want)
			}
		})
	}
}

func TestEngineCommandPrefixRulesAreTokenBoundedAndRejectShellChaining(t *testing.T) {
	root := testRoot(t)
	engine := testEngine(t, root, ModeHeadless, func(options *Options) {
		options.CLI = []Rule{{
			Name: "go-test", Tool: "shell", Effects: []tools.Effect{tools.EffectExecute},
			CommandPrefixes: []string{"go test"}, Decision: DecisionAllow,
		}}
	})
	for command, want := range map[string]Decision{
		"go test ./...":             DecisionAllow,
		"go   test ./internal/app":  DecisionAllow,
		"go testing ./...":          DecisionDeny,
		"go test ./... && echo bad": DecisionDeny,
		"go test ./...; echo bad":   DecisionDeny,
	} {
		t.Run(command, func(t *testing.T) {
			call, err := tools.PrepareCall("call", "shell", mustPolicyJSON(t, map[string]any{
				"command": command,
			}), []tools.Effect{tools.EffectExecute}, nil, tools.Preview{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := engine.Evaluate(call); got.Decision != want {
				t.Fatalf("decision=%q rule=%q want=%q", got.Decision, got.Rule, want)
			}
		})
	}
}

func mustPolicyJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestEngineRulePriority(t *testing.T) {
	root := testRoot(t)
	engine := testEngine(t, root, ModeInteractive, func(options *Options) {
		options.CLI = []Rule{{Name: "temporary", Effects: []tools.Effect{tools.EffectWrite}, Decision: DecisionDeny}}
		options.Profile = []Rule{{Name: "profile", Effects: []tools.Effect{tools.EffectWrite}, Decision: DecisionAllow}}
		options.ToolDefaults = []Rule{{Name: "tool", Effects: []tools.Effect{tools.EffectWrite}, Decision: DecisionAllow}}
	})
	result := engine.Evaluate(testCall(t, root, []tools.Effect{tools.EffectWrite}, "file.txt", false))
	if result.Decision != DecisionDeny || result.Rule != "cli.temporary" {
		t.Fatalf("result = %+v", result)
	}

	engine = testEngine(t, root, ModeInteractive, func(options *Options) {
		options.Profile = []Rule{{Name: "profile", Decision: DecisionDeny}}
		options.ToolDefaults = []Rule{{Name: "tool", Decision: DecisionAllow}}
	})
	result = engine.Evaluate(testCall(t, root, []tools.Effect{tools.EffectRead}, "file.txt", false))
	if result.Decision != DecisionDeny || result.Rule != "profile.profile" {
		t.Fatalf("result = %+v", result)
	}
}

func TestEngineHardDenyCannotBeOverridden(t *testing.T) {
	root := testRoot(t)
	allow := []Rule{{Name: "allow-all", Decision: DecisionAllow}}
	for name, call := range map[string]*tools.PreparedCall{
		"network":            testCall(t, root, []tools.Effect{tools.EffectNetwork}, "", false),
		"outside root":       testCall(t, root, []tools.Effect{tools.EffectRead}, filepath.Join(filepath.Dir(root), "outside.txt"), false),
		"protected write":    testCall(t, root, []tools.Effect{tools.EffectWrite}, ".git/config", true),
		"headless sensitive": testCall(t, root, []tools.Effect{tools.EffectRead}, ".env", true),
	} {
		t.Run(name, func(t *testing.T) {
			engine := testEngine(t, root, ModeHeadless, func(options *Options) { options.CLI = allow })
			if result := engine.Evaluate(call); result.Decision != DecisionDeny || result.Rule[:5] != "hard." {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestEngineRuleMatchesToolEffectAndSensitivity(t *testing.T) {
	root := testRoot(t)
	engine := testEngine(t, root, ModeInteractive, func(options *Options) {
		options.CLI = []Rule{{
			Name: "sensitive-read", Tool: "test_tool", Effects: []tools.Effect{tools.EffectRead},
			Sensitive: boolPointer(true), Decision: DecisionDeny,
		}}
	})
	if got := engine.Evaluate(testCall(t, root, []tools.Effect{tools.EffectRead}, ".env", true)); got.Rule != "cli.sensitive-read" {
		t.Fatalf("result = %+v", got)
	}
	if got := engine.Evaluate(testCall(t, root, []tools.Effect{tools.EffectRead}, "main.go", false)); got.Decision != DecisionAllow {
		t.Fatalf("result = %+v", got)
	}
}

func TestEngineAcceptsPathGuardRootWhenConfiguredRootHasAlias(t *testing.T) {
	rawRoot := t.TempDir()
	guard, err := platform.NewPathGuard(rawRoot)
	if err != nil {
		t.Fatal(err)
	}
	engine := testEngine(t, rawRoot, ModeInteractive, nil)
	result := engine.Evaluate(testCall(t, guard.Root(), []tools.Effect{tools.EffectRead}, "main.go", false))
	if result.Decision != DecisionAllow {
		t.Fatalf("result = %+v; raw_root=%q canonical_root=%q", result, rawRoot, guard.Root())
	}
}
