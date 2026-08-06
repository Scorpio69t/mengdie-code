// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const dataDirEnvironment = "MENGDIE_DATA_DIR"

// DataDirOptions keeps platform and environment discovery injectable for
// cross-platform contract tests.
type DataDirOptions struct {
	Override    string
	ProjectRoot string
	GOOS        string
	LookupEnv   func(string) (string, bool)
	UserHomeDir func() (string, error)
}

// ResolveDataDir returns a validated absolute local data directory without
// creating it.
func ResolveDataDir(options DataDirOptions) (string, error) {
	lookup := options.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	userHome := options.UserHomeDir
	if userHome == nil {
		userHome = os.UserHomeDir
	}
	goos := options.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}

	directory := strings.TrimSpace(options.Override)
	if directory == "" {
		if configured, ok := lookup(dataDirEnvironment); ok && strings.TrimSpace(configured) != "" {
			directory = strings.TrimSpace(configured)
		}
	}
	if directory == "" {
		var err error
		directory, err = defaultDataDir(goos, lookup, userHome)
		if err != nil {
			return "", err
		}
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve MengDie data directory: %w", err)
	}
	abs = filepath.Clean(abs)
	if err := validateDataDir(abs, options.ProjectRoot); err != nil {
		return "", err
	}
	return abs, nil
}

func defaultDataDir(goos string, lookup func(string) (string, bool), userHome func() (string, error)) (string, error) {
	switch goos {
	case "windows":
		local, ok := lookup("LOCALAPPDATA")
		if !ok || strings.TrimSpace(local) == "" {
			return "", errors.New("resolve MengDie data directory: LOCALAPPDATA is unavailable")
		}
		return joinForOS(goos, strings.TrimSpace(local), "MengDie Code"), nil
	case "darwin":
		home, err := userHome()
		if err != nil {
			return "", fmt.Errorf("resolve MengDie data directory home: %w", err)
		}
		if strings.TrimSpace(home) == "" {
			return "", errors.New("resolve MengDie data directory: home is unavailable")
		}
		return joinForOS(goos, home, "Library", "Application Support", "MengDie Code"), nil
	case "linux":
		if state, ok := lookup("XDG_STATE_HOME"); ok && strings.TrimSpace(state) != "" {
			return joinForOS(goos, strings.TrimSpace(state), "mengdie"), nil
		}
		home, err := userHome()
		if err != nil {
			return "", fmt.Errorf("resolve MengDie data directory home: %w", err)
		}
		if strings.TrimSpace(home) == "" {
			return "", errors.New("resolve MengDie data directory: home is unavailable")
		}
		return joinForOS(goos, home, ".local", "state", "mengdie"), nil
	default:
		return "", fmt.Errorf("resolve MengDie data directory: unsupported platform %q", goos)
	}
}

func joinForOS(goos string, elements ...string) string {
	if goos != "windows" {
		return path.Join(elements...)
	}
	result := strings.TrimRight(elements[0], `\/`)
	for _, element := range elements[1:] {
		result += `\` + strings.Trim(element, `\/`)
	}
	return result
}

func validateDataDir(directory, projectRoot string) error {
	if filepath.Dir(directory) == directory {
		return errors.New("MengDie data directory cannot be a filesystem root")
	}
	normalized := strings.ToLower(strings.ReplaceAll(directory, `\`, "/"))
	if strings.HasPrefix(directory, `\\`) || strings.HasPrefix(directory, "//") {
		return errors.New("MengDie data directory cannot be a network share")
	}
	for _, component := range strings.Split(normalized, "/") {
		if component == "onedrive" || strings.HasPrefix(component, "onedrive - ") ||
			component == "icloud drive" || component == "iclouddrive" || component == "com~apple~clouddocs" {
			return errors.New("MengDie data directory cannot be inside a synchronized cloud directory")
		}
	}
	if strings.TrimSpace(projectRoot) != "" {
		root, err := filepath.Abs(projectRoot)
		if err != nil {
			return fmt.Errorf("resolve project root for data directory: %w", err)
		}
		relative, err := filepath.Rel(filepath.Clean(root), directory)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("MengDie data directory cannot be inside the project root")
		}
	}
	if info, err := os.Lstat(directory); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("MengDie data directory cannot be a symbolic link")
		}
		if !info.IsDir() {
			return errors.New("MengDie data directory path is not a directory")
		}
		if reparse, reparseErr := isReparsePoint(directory); reparseErr != nil {
			return reparseErr
		} else if reparse {
			return errors.New("MengDie data directory cannot be a reparse point")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect MengDie data directory: %w", err)
	}
	return nil
}

func prepareDataDir(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create MengDie data directory: %w", err)
	}
	if err := validateDataDir(directory, ""); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("set MengDie data directory permissions: %w", err)
		}
	}
	return nil
}

func protectDataFile(filename string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if err := os.Chmod(filename, 0o600); err != nil {
		return fmt.Errorf("set MengDie database permissions: %w", err)
	}
	return nil
}
