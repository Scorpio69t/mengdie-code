// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package evaluation provides the repository-local coding evaluation harness.
package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const SchemaVersion = 1

// Manifest describes a reproducible collection of coding fixtures.
type Manifest struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Description   string `json:"description"`
	FixtureRoot   string `json:"fixture_root"`
	Tasks         []Task `json:"tasks"`
}

// Task describes one isolated repository state and its verification command.
type Task struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Prompt   string       `json:"prompt"`
	Fixture  string       `json:"fixture"`
	Tags     []string     `json:"tags,omitempty"`
	Verify   VerifySpec   `json:"verify"`
	Baseline BaselineSpec `json:"baseline"`
}

// VerifySpec is an argv-based command. Shell interpolation is never applied.
type VerifySpec struct {
	Command []string `json:"command"`
	Timeout string   `json:"timeout"`
}

// BaselineSpec defines the expected verifier state before an agent edits it.
type BaselineSpec struct {
	ExpectedExitCode int `json:"expected_exit_code"`
}

// LoadManifest reads and strictly validates one manifest.
func LoadManifest(path string) (manifest Manifest, err error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("open evaluation manifest: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			manifest = Manifest{}
			err = errors.Join(err, fmt.Errorf("close evaluation manifest: %w", closeErr))
		}
	}()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode evaluation manifest: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return Manifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing manifest data: %w", err)
	}
	return errors.New("evaluation manifest contains multiple JSON values")
}

// Validate checks schema and task invariants without touching fixture files.
func (m Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported evaluation schema_version %d, want %d", m.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(m.ID) == "" {
		return errors.New("evaluation manifest id is required")
	}
	if strings.TrimSpace(m.FixtureRoot) == "" {
		return errors.New("evaluation fixture_root is required")
	}
	if len(m.Tasks) == 0 {
		return errors.New("evaluation manifest must contain at least one task")
	}

	seen := make(map[string]struct{}, len(m.Tasks))
	for index, task := range m.Tasks {
		if err := task.validate(); err != nil {
			return fmt.Errorf("task %d: %w", index, err)
		}
		if _, exists := seen[task.ID]; exists {
			return fmt.Errorf("duplicate evaluation task id %q", task.ID)
		}
		seen[task.ID] = struct{}{}
	}
	return nil
}

func (t Task) validate() error {
	if strings.TrimSpace(t.ID) == "" {
		return errors.New("id is required")
	}
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("task %q title is required", t.ID)
	}
	if strings.TrimSpace(t.Prompt) == "" {
		return fmt.Errorf("task %q prompt is required", t.ID)
	}
	if strings.TrimSpace(t.Fixture) == "" {
		return fmt.Errorf("task %q fixture is required", t.ID)
	}
	if len(t.Verify.Command) == 0 || strings.TrimSpace(t.Verify.Command[0]) == "" {
		return fmt.Errorf("task %q verify.command is required", t.ID)
	}
	if _, err := t.Verify.duration(); err != nil {
		return fmt.Errorf("task %q: %w", t.ID, err)
	}
	return nil
}

func (v VerifySpec) duration() (time.Duration, error) {
	if strings.TrimSpace(v.Timeout) == "" {
		return 30 * time.Second, nil
	}
	timeout, err := time.ParseDuration(v.Timeout)
	if err != nil {
		return 0, fmt.Errorf("invalid verify timeout %q: %w", v.Timeout, err)
	}
	if timeout <= 0 {
		return 0, errors.New("verify timeout must be greater than zero")
	}
	return timeout, nil
}

func resolveFixtureRoot(manifestPath, fixtureRoot string) (string, error) {
	manifestPath, err := filepath.Abs(manifestPath)
	if err != nil {
		return "", fmt.Errorf("resolve manifest path: %w", err)
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(fixtureRoot)))
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("stat fixture root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fixture root %q is not a directory", root)
	}
	return root, nil
}

func resolveFixture(root, relative string) (string, error) {
	path := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("resolve fixture %q: %w", relative, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("fixture %q escapes fixture_root", relative)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat fixture %q: %w", relative, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fixture %q is not a directory", relative)
	}
	return path, nil
}
