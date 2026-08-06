// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestExampleConfigLoads(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	examplesRoot := filepath.Join(filepath.Dir(sourceFile), "..", "..", "configs", "examples")
	for name, want := range map[string]struct{ profile, model string }{
		"config.toml":   {profile: "deepseek", model: "deepseek-v4-flash"},
		"deepseek.toml": {profile: "deepseek", model: "deepseek-v4-flash"},
		"kimi.toml":     {profile: "kimi", model: "kimi-k3"},
	} {
		t.Run(name, func(t *testing.T) {
			example, err := os.ReadFile(filepath.Join(examplesRoot, name))
			if err != nil {
				t.Fatalf("read example config: %v", err)
			}
			root := t.TempDir()
			writeTestConfig(t, filepath.Join(root, ".mengdie", "config.toml"), string(example))
			loaded, err := Load(Options{WorkDir: root, UserConfigDir: t.TempDir(), LookupEnv: mapLookup(nil)})
			if err != nil {
				t.Fatalf("Load() example error = %v", err)
			}
			if loaded.SelectedProfile != want.profile || loaded.Profile().Model != want.model {
				t.Fatalf("loaded example = %+v", loaded)
			}
		})
	}
}

func TestLoadPrecedence(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	userDir := t.TempDir()
	writeTestConfig(t, filepath.Join(userDir, "mengdie", "config.toml"), `
default_profile = "work"

[profiles.work]
provider = "user-provider"
base_url = "https://user.example/v1"
api_key_env = "USER_KEY"
model = "user-model"
request_timeout = "90s"
max_context_tokens = 32000

[context]
max_turns = 20
`)
	writeTestConfig(t, filepath.Join(root, ".mengdie", "config.toml"), `
[profiles.work]
model = "project-model"

[approval]
mode = "suggest"
`)
	environment := map[string]string{
		"MENGDIE_MODEL":     "env-provider:env-model",
		"MENGDIE_MAX_TURNS": "40",
	}

	loaded, err := Load(Options{
		WorkDir:          root,
		UserConfigDir:    userDir,
		LookupEnv:        mapLookup(environment),
		ModelOverride:    "cli-provider:cli-model",
		ApprovalOverride: ApprovalAutoEdit,
		MaxTurnsOverride: 50,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	profile := loaded.Profile()
	if profile.Provider != "cli-provider" || profile.Model != "cli-model" {
		t.Fatalf("profile = %+v, want CLI model override", profile)
	}
	if profile.BaseURL != "https://user.example/v1" || profile.APIKeyEnv != "USER_KEY" {
		t.Fatalf("profile = %+v, want values inherited from user config", profile)
	}
	if profile.RequestTimeout != 90*time.Second {
		t.Fatalf("request timeout = %s, want 90s", profile.RequestTimeout)
	}
	if loaded.Config.Approval.Mode != ApprovalAutoEdit {
		t.Fatalf("approval mode = %q", loaded.Config.Approval.Mode)
	}
	if loaded.Config.Context.MaxTurns != 50 {
		t.Fatalf("max turns = %d, want 50", loaded.Config.Context.MaxTurns)
	}
	if !loaded.UserConfigLoaded || !loaded.ProjectConfigLoaded {
		t.Fatalf("loaded metadata = %+v, want both files loaded", loaded)
	}
}

func TestLoadEnvironmentOverridesProject(t *testing.T) {
	root := t.TempDir()
	writeTestConfig(t, filepath.Join(root, ".mengdie", "config.toml"), `
[profiles.default]
provider = "project-provider"
model = "project-model"
`)

	loaded, err := Load(Options{
		WorkDir:       root,
		UserConfigDir: t.TempDir(),
		LookupEnv: mapLookup(map[string]string{
			"MENGDIE_MODEL": "env-provider:env-model",
		}),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := loaded.Profile(); got.Provider != "env-provider" || got.Model != "env-model" {
		t.Fatalf("profile = %+v, want environment override", got)
	}
}

func TestLoadRejectsInlineSecret(t *testing.T) {
	root := t.TempDir()
	writeTestConfig(t, filepath.Join(root, ".mengdie", "config.toml"), `
[profiles.default]
provider = "openai-compatible"
model = "test-model"
api_key = "must-not-load"
`)

	if _, err := Load(Options{WorkDir: root, UserConfigDir: t.TempDir(), LookupEnv: mapLookup(nil)}); err == nil {
		t.Fatal("Load() error = nil, want inline secret rejection")
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	root := t.TempDir()
	writeTestConfig(t, filepath.Join(root, ".mengdie", "config.toml"), "unexpected = true\n")

	if _, err := Load(Options{WorkDir: root, UserConfigDir: t.TempDir(), LookupEnv: mapLookup(nil)}); err == nil {
		t.Fatal("Load() error = nil, want strict TOML error")
	}
}

func TestLoadRejectsMissingSelectedProfile(t *testing.T) {
	root := t.TempDir()
	if _, err := Load(Options{
		WorkDir:         root,
		UserConfigDir:   t.TempDir(),
		LookupEnv:       mapLookup(nil),
		ProfileOverride: "missing",
	}); err == nil {
		t.Fatal("Load() error = nil, want missing profile error")
	}
}

func writeTestConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
