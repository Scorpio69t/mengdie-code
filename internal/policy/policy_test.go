// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func boolPointer(value bool) *bool { return &value }

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
	root := t.TempDir()
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

func TestEngineRulePriority(t *testing.T) {
	root := t.TempDir()
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
	root := t.TempDir()
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
	root := t.TempDir()
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
