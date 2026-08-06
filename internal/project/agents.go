// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxAgentsFileBytes  = 64 << 10
	maxAgentsTotalBytes = 256 << 10
)

type Instruction struct {
	Path    string
	Content string
}

type AgentsOptions struct {
	UserConfigDir string
	ProjectRoot   string
	WorkDir       string
}

// LoadAgents loads optional user and project AGENTS.md files from broadest to
// nearest scope. Repository instructions are model context, never authority.
func LoadAgents(options AgentsOptions) ([]Instruction, error) {
	root, err := canonicalDirectory(options.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("agents: project root: %w", err)
	}
	workDir, err := canonicalDirectory(options.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("agents: working directory: %w", err)
	}
	relative, err := filepath.Rel(root, workDir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, errors.New("agents: working directory escapes project root")
	}

	var candidates []string
	if strings.TrimSpace(options.UserConfigDir) != "" {
		candidates = append(candidates, filepath.Join(options.UserConfigDir, "AGENTS.md"))
	}
	candidates = append(candidates, filepath.Join(root, "AGENTS.md"))
	if relative != "." {
		current := root
		for _, part := range strings.Split(relative, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			candidates = append(candidates, filepath.Join(current, "AGENTS.md"))
		}
	}

	seen := make(map[string]struct{}, len(candidates))
	var result []Instruction
	total := 0
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if _, duplicate := seen[candidate]; duplicate {
			continue
		}
		seen[candidate] = struct{}{}
		content, exists, err := readOptionalInstruction(candidate)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		total += len(content)
		if total > maxAgentsTotalBytes {
			return nil, fmt.Errorf("agents: instruction chain exceeds %d-byte limit", maxAgentsTotalBytes)
		}
		result = append(result, Instruction{Path: candidate, Content: content})
	}
	return result, nil
}

func readOptionalInstruction(path string) (contentResult string, exists bool, resultErr error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("agents: open %s: %w", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("agents: close %s: %w", path, err))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return "", false, fmt.Errorf("agents: stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("agents: %s is not a regular file", path)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxAgentsFileBytes+1))
	if err != nil {
		return "", false, fmt.Errorf("agents: read %s: %w", path, err)
	}
	if len(content) > maxAgentsFileBytes {
		return "", false, fmt.Errorf("agents: %s exceeds %d-byte limit", path, maxAgentsFileBytes)
	}
	if !utf8.Valid(content) {
		return "", false, fmt.Errorf("agents: %s is not UTF-8 text", path)
	}
	return string(content), true, nil
}

func canonicalDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		absolute = filepath.Dir(absolute)
	}
	return filepath.EvalSymlinks(absolute)
}
