// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultDataDirContracts(t *testing.T) {
	lookup := func(name string) (string, bool) {
		values := map[string]string{
			"LOCALAPPDATA":   `C:\Users\tester\AppData\Local`,
			"XDG_STATE_HOME": "/state",
		}
		value, ok := values[name]
		return value, ok
	}
	home := func() (string, error) { return "/Users/tester", nil }
	tests := []struct {
		goos string
		want string
	}{
		{goos: "windows", want: `C:\Users\tester\AppData\Local\MengDie Code`},
		{goos: "darwin", want: "/Users/tester/Library/Application Support/MengDie Code"},
		{goos: "linux", want: "/state/mengdie"},
	}
	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			got, err := defaultDataDir(test.goos, lookup, home)
			if err != nil || got != test.want {
				t.Fatalf("defaultDataDir()=(%q, %v), want %q", got, err, test.want)
			}
		})
	}
}

func TestResolveDataDirOverrideAndEnvironment(t *testing.T) {
	override := filepath.Join(t.TempDir(), "explicit")
	got, err := ResolveDataDir(DataDirOptions{Override: override})
	if err != nil || got != filepath.Clean(override) {
		t.Fatalf("ResolveDataDir(override)=(%q, %v)", got, err)
	}
	environment := filepath.Join(t.TempDir(), "environment")
	got, err = ResolveDataDir(DataDirOptions{LookupEnv: func(name string) (string, bool) {
		if name == dataDirEnvironment {
			return environment, true
		}
		return "", false
	}})
	if err != nil || got != filepath.Clean(environment) {
		t.Fatalf("ResolveDataDir(environment)=(%q, %v)", got, err)
	}
}

func TestDataDirRejectsUnsafeLocations(t *testing.T) {
	project := t.TempDir()
	tests := []struct {
		name string
		path string
		root string
	}{
		{name: "filesystem root", path: filepath.VolumeName(project) + string(filepath.Separator)},
		{name: "project directory", path: filepath.Join(project, ".mengdie-data"), root: project},
		{name: "network share", path: `\\server\share\mengdie`},
		{name: "onedrive", path: filepath.Join(filepath.Dir(project), "OneDrive", "mengdie")},
		{name: "icloud display name", path: filepath.Join(filepath.Dir(project), "iCloud Drive", "mengdie")},
		{name: "icloud windows", path: filepath.Join(filepath.Dir(project), "iCloudDrive", "mengdie")},
		{name: "icloud macos", path: filepath.Join(filepath.Dir(project), "Mobile Documents", "com~apple~CloudDocs", "mengdie")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateDataDir(test.path, test.root); err == nil {
				t.Fatalf("validateDataDir(%q) succeeded", test.path)
			}
		})
	}
}

func TestDataDirRejectsSymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "data-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := validateDataDir(link, ""); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("validateDataDir(symlink)=%v", err)
	}
}

func TestPrepareDataDirUsesPrivateUnixPermissions(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := prepareDataDir(directory); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode=%o, want 700", info.Mode().Perm())
	}
}

func TestDefaultDataDirReportsUnavailablePlatformState(t *testing.T) {
	_, err := defaultDataDir("windows", func(string) (string, bool) { return "", false }, nil)
	if err == nil {
		t.Fatal("defaultDataDir(windows) succeeded without LOCALAPPDATA")
	}
	_, err = defaultDataDir("plan9", nil, nil)
	if err == nil {
		t.Fatal("defaultDataDir(plan9) succeeded")
	}
	_, err = defaultDataDir("darwin", nil, func() (string, error) { return "", errors.New("home failed") })
	if !strings.Contains(err.Error(), "home failed") {
		t.Fatalf("defaultDataDir(darwin)=%v", err)
	}
}
