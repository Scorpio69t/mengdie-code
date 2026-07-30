// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate checks the fully merged configuration and selected profile.
func (l Loaded) Validate() error {
	if strings.TrimSpace(l.SelectedProfile) == "" {
		return errors.New("selected profile is empty")
	}
	profile, exists := l.Config.Profiles[l.SelectedProfile]
	if !exists {
		return fmt.Errorf("profile %q is not defined", l.SelectedProfile)
	}
	if err := validateProfile(l.SelectedProfile, profile); err != nil {
		return err
	}
	switch l.Config.Approval.Mode {
	case ApprovalSuggest, ApprovalAutoEdit:
	default:
		return fmt.Errorf("approval mode %q must be suggest or auto-edit", l.Config.Approval.Mode)
	}
	if l.Config.Context.MaxTurns < 1 || l.Config.Context.MaxTurns > 256 {
		return fmt.Errorf("context max_turns must be between 1 and 256")
	}
	if l.Config.Context.MaxToolOutputBytes < 1024 {
		return errors.New("context max_tool_output_bytes must be at least 1024")
	}
	return nil
}

func validateProfile(name string, profile Profile) error {
	configured := profile.Provider != "" || profile.BaseURL != "" || profile.Model != "" || profile.APIKeyEnv != ""
	if configured && (profile.Provider == "" || profile.Model == "") {
		return fmt.Errorf("profile %q must define both provider and model", name)
	}
	if profile.BaseURL != "" {
		parsed, err := url.Parse(profile.BaseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return fmt.Errorf("profile %q base_url must be an absolute HTTP(S) URL", name)
		}
	}
	if profile.APIKeyEnv != "" && !environmentName.MatchString(profile.APIKeyEnv) {
		return fmt.Errorf("profile %q api_key_env %q is not a valid environment variable name", name, profile.APIKeyEnv)
	}
	if profile.RequestTimeout <= 0 {
		return fmt.Errorf("profile %q request_timeout must be greater than zero", name)
	}
	if profile.MaxContextTokens < 0 {
		return fmt.Errorf("profile %q max_context_tokens cannot be negative", name)
	}
	return nil
}

func parseDuration(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, errors.New("duration must be greater than zero")
	}
	return duration, nil
}
