// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package config loads and validates layered MengDie configuration.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultProfileName = "default"
	ApprovalSuggest    = "suggest"
	ApprovalAutoEdit   = "auto-edit"
)

// Config is the fully merged, validated application configuration.
type Config struct {
	DefaultProfile string
	Profiles       map[string]Profile
	Approval       Approval
	Context        Context
}

// Profile describes one model endpoint without containing credential values.
type Profile struct {
	Provider         string
	BaseURL          string
	APIKeyEnv        string
	Model            string
	CheapModel       string
	RequestTimeout   time.Duration
	MaxContextTokens int
}

// Approval controls the deterministic tool authorization policy.
type Approval struct {
	Mode             string
	ReadProjectFiles bool
	AllowCommands    []string
}

// Context contains bounded runtime limits shared by providers and tools.
type Context struct {
	MaxToolOutputBytes int
	MaxTurns           int
}

// Defaults returns a fresh configuration with no provider credentials.
func Defaults() Config {
	return Config{
		DefaultProfile: DefaultProfileName,
		Profiles: map[string]Profile{
			DefaultProfileName: {
				RequestTimeout:   120 * time.Second,
				MaxContextTokens: 64_000,
			},
		},
		Approval: Approval{
			Mode:             ApprovalSuggest,
			ReadProjectFiles: true,
			AllowCommands:    []string{"go test", "go vet", "git status", "git diff"},
		},
		Context: Context{
			MaxToolOutputBytes: 64 << 10,
			MaxTurns:           32,
		},
	}
}

// Loaded includes resolved paths and layer metadata for doctor output.
type Loaded struct {
	Config              Config
	SelectedProfile     string
	ProjectRoot         string
	WorkingDir          string
	UserConfigPath      string
	ProjectConfigPath   string
	UserConfigLoaded    bool
	ProjectConfigLoaded bool
	// ProjectIdentity is the explicit identity used by memory tooling; when empty,
	// ProjectIdentityValue falls back to the base name of ProjectRoot.
	ProjectIdentity string
}

// Profile returns the selected profile after validation.
func (l Loaded) Profile() Profile {
	return l.Config.Profiles[l.SelectedProfile]
}

// ProjectIdentityValue returns the explicit ProjectIdentity when set,
// otherwise falls back to filepath.Base(ProjectRoot). Both empty inputs
// return "" so callers can disable the field by zero-loading it.
func (l Loaded) ProjectIdentityValue() string {
	if l.ProjectIdentity != "" {
		return l.ProjectIdentity
	}
	trimmed := strings.TrimRight(l.ProjectRoot, string(os.PathSeparator))
	if trimmed == "" {
		return ""
	}
	return filepath.Base(trimmed)
}

// Options provides process-specific paths, environment, and CLI overrides.
type Options struct {
	WorkDir          string
	UserConfigDir    string
	LookupEnv        func(string) (string, bool)
	ProfileOverride  string
	ModelOverride    string
	ApprovalOverride string
	MaxTurnsOverride int
}
