// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/project"
	"github.com/pelletier/go-toml/v2"
)

type fileConfig struct {
	DefaultProfile *string                `toml:"default_profile"`
	Profiles       map[string]fileProfile `toml:"profiles"`
	Approval       fileApproval           `toml:"approval"`
	Context        fileContext            `toml:"context"`
}

type fileProfile struct {
	Provider         *string `toml:"provider"`
	BaseURL          *string `toml:"base_url"`
	APIKeyEnv        *string `toml:"api_key_env"`
	APIKey           *string `toml:"api_key"`
	Model            *string `toml:"model"`
	CheapModel       *string `toml:"cheap_model"`
	RequestTimeout   *string `toml:"request_timeout"`
	MaxContextTokens *int    `toml:"max_context_tokens"`
}

type fileApproval struct {
	Mode             *string   `toml:"mode"`
	ReadProjectFiles *bool     `toml:"read_project_files"`
	AllowCommands    *[]string `toml:"allow_commands"`
}

type fileContext struct {
	MaxToolOutputBytes *int `toml:"max_tool_output_bytes"`
	MaxTurns           *int `toml:"max_turns"`
}

// Load applies defaults, user config, project config, environment, and CLI
// overrides in ascending precedence order.
func Load(options Options) (Loaded, error) {
	lookupEnv := options.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	root, err := project.FindRoot(options.WorkDir)
	if err != nil {
		return Loaded{}, err
	}
	workingDir := options.WorkDir
	if strings.TrimSpace(workingDir) == "" {
		workingDir, err = os.Getwd()
		if err != nil {
			return Loaded{}, fmt.Errorf("get working directory: %w", err)
		}
	}
	workingDir, err = filepath.Abs(workingDir)
	if err != nil {
		return Loaded{}, fmt.Errorf("resolve working directory: %w", err)
	}
	if info, statErr := os.Stat(workingDir); statErr == nil && !info.IsDir() {
		workingDir = filepath.Dir(workingDir)
	} else if statErr != nil {
		return Loaded{}, fmt.Errorf("stat working directory: %w", statErr)
	}
	userDir := options.UserConfigDir
	if userDir == "" {
		userDir, err = os.UserConfigDir()
		if err != nil {
			return Loaded{}, fmt.Errorf("resolve user config directory: %w", err)
		}
	}

	loaded := Loaded{
		Config:            Defaults(),
		ProjectRoot:       root,
		WorkingDir:        filepath.Clean(workingDir),
		UserConfigPath:    filepath.Join(userDir, "mengdie", "config.toml"),
		ProjectConfigPath: filepath.Join(root, ".mengdie", "config.toml"),
	}
	if loaded.UserConfigLoaded, err = applyOptionalFile(&loaded.Config, loaded.UserConfigPath); err != nil {
		return Loaded{}, fmt.Errorf("load user config: %w", err)
	}
	if loaded.ProjectConfigLoaded, err = applyOptionalFile(&loaded.Config, loaded.ProjectConfigPath); err != nil {
		return Loaded{}, fmt.Errorf("load project config: %w", err)
	}
	if err := applyEnvironment(&loaded.Config, lookupEnv); err != nil {
		return Loaded{}, err
	}

	selected := loaded.Config.DefaultProfile
	if value, ok := lookupEnv("MENGDIE_PROFILE"); ok && strings.TrimSpace(value) != "" {
		selected = strings.TrimSpace(value)
	}
	if options.ProfileOverride != "" {
		selected = options.ProfileOverride
	}
	if options.ModelOverride != "" {
		if profile, exists := loaded.Config.Profiles[selected]; exists {
			applyModel(&profile, options.ModelOverride)
			loaded.Config.Profiles[selected] = profile
		}
	}
	if options.ApprovalOverride != "" {
		loaded.Config.Approval.Mode = options.ApprovalOverride
	}
	if options.MaxTurnsOverride != 0 {
		loaded.Config.Context.MaxTurns = options.MaxTurnsOverride
	}
	loaded.SelectedProfile = selected
	if err := loaded.Validate(); err != nil {
		return Loaded{}, err
	}
	return loaded, nil
}

func applyOptionalFile(config *Config, path string) (loaded bool, err error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			loaded = false
			err = errors.Join(err, fmt.Errorf("close config file: %w", closeErr))
		}
	}()

	var layer fileConfig
	decoder := toml.NewDecoder(file).DisallowUnknownFields()
	if err := decoder.Decode(&layer); err != nil {
		return false, err
	}
	if err := applyFile(config, layer); err != nil {
		return false, err
	}
	return true, nil
}

func applyFile(config *Config, layer fileConfig) error {
	if layer.DefaultProfile != nil {
		config.DefaultProfile = strings.TrimSpace(*layer.DefaultProfile)
	}
	for name, source := range layer.Profiles {
		if source.APIKey != nil {
			return fmt.Errorf("profile %q contains forbidden api_key; use api_key_env", name)
		}
		profile := config.Profiles[name]
		if source.Provider != nil {
			profile.Provider = strings.TrimSpace(*source.Provider)
		}
		if source.BaseURL != nil {
			profile.BaseURL = strings.TrimSpace(*source.BaseURL)
		}
		if source.APIKeyEnv != nil {
			profile.APIKeyEnv = strings.TrimSpace(*source.APIKeyEnv)
		}
		if source.Model != nil {
			profile.Model = strings.TrimSpace(*source.Model)
		}
		if source.CheapModel != nil {
			profile.CheapModel = strings.TrimSpace(*source.CheapModel)
		}
		if source.RequestTimeout != nil {
			timeout, err := parseDuration(*source.RequestTimeout)
			if err != nil {
				return fmt.Errorf("profile %q request_timeout: %w", name, err)
			}
			profile.RequestTimeout = timeout
		}
		if source.MaxContextTokens != nil {
			profile.MaxContextTokens = *source.MaxContextTokens
		}
		config.Profiles[name] = profile
	}
	if layer.Approval.Mode != nil {
		config.Approval.Mode = strings.TrimSpace(*layer.Approval.Mode)
	}
	if layer.Approval.ReadProjectFiles != nil {
		config.Approval.ReadProjectFiles = *layer.Approval.ReadProjectFiles
	}
	if layer.Approval.AllowCommands != nil {
		config.Approval.AllowCommands = append([]string(nil), (*layer.Approval.AllowCommands)...)
	}
	if layer.Context.MaxToolOutputBytes != nil {
		config.Context.MaxToolOutputBytes = *layer.Context.MaxToolOutputBytes
	}
	if layer.Context.MaxTurns != nil {
		config.Context.MaxTurns = *layer.Context.MaxTurns
	}
	return nil
}

func applyEnvironment(config *Config, lookup func(string) (string, bool)) error {
	profileName := config.DefaultProfile
	if value, ok := lookup("MENGDIE_PROFILE"); ok && strings.TrimSpace(value) != "" {
		profileName = strings.TrimSpace(value)
	}
	profile, exists := config.Profiles[profileName]
	if !exists {
		return fmt.Errorf("profile %q selected by environment or default_profile is not defined", profileName)
	}
	if value, ok := lookup("MENGDIE_MODEL"); ok && strings.TrimSpace(value) != "" {
		applyModel(&profile, value)
	}
	if value, ok := lookup("MENGDIE_BASE_URL"); ok {
		profile.BaseURL = strings.TrimSpace(value)
	}
	if value, ok := lookup("MENGDIE_API_KEY_ENV"); ok {
		profile.APIKeyEnv = strings.TrimSpace(value)
	}
	config.Profiles[profileName] = profile

	if value, ok := lookup("MENGDIE_APPROVAL"); ok {
		config.Approval.Mode = strings.TrimSpace(value)
	}
	if value, ok := lookup("MENGDIE_MAX_TURNS"); ok && strings.TrimSpace(value) != "" {
		maxTurns, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("MENGDIE_MAX_TURNS must be an integer: %w", err)
		}
		config.Context.MaxTurns = maxTurns
	}
	return nil
}

func applyModel(profile *Profile, value string) {
	value = strings.TrimSpace(value)
	if provider, model, found := strings.Cut(value, ":"); found {
		profile.Provider = strings.TrimSpace(provider)
		profile.Model = strings.TrimSpace(model)
		return
	}
	profile.Model = value
}
