// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package skills discovers bounded local SKILL.md instructions. Skills are
// model context only: they never grant filesystem, process, network, or other
// tool authority.
package skills

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxSkillFileBytes   = 48 << 10
	maxSkillTotalBytes  = 1 << 20
	maxSkills           = 64
	maxDescriptionBytes = 2 << 10
)

type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

type Skill struct {
	Name        string
	Description string
	Source      string
	Scope       Scope
	Path        string
	SHA256      string
	Size        int
}

type Conflict struct {
	Name          string
	WinnerSource  string
	IgnoredSource string
}

type Catalog struct {
	Skills    []Skill
	Conflicts []Conflict
}

type Options struct {
	UserHomeDir string
	ProjectRoot string
}

// Discover loads only bounded metadata from fixed user and project Skill
// roots. Project Skills deterministically replace user Skills with the same
// portable name; full content is loaded only through read_skill.
func Discover(options Options) (Catalog, error) {
	projectRoot, err := canonicalDirectory(options.ProjectRoot)
	if err != nil {
		return Catalog{}, fmt.Errorf("skills: project root: %w", err)
	}
	roots := []skillRoot{}
	if strings.TrimSpace(options.UserHomeDir) != "" {
		roots = append(roots, skillRoot{
			scope: ScopeUser,
			path:  filepath.Join(options.UserHomeDir, ".mengdie", "skills"),
			label: "~/.mengdie/skills",
		})
	}
	roots = append(roots, skillRoot{
		scope: ScopeProject,
		path:  filepath.Join(projectRoot, ".mengdie", "skills"),
		label: "$PROJECT_ROOT/.mengdie/skills",
	})

	selected := make(map[string]Skill)
	var conflicts []Conflict
	total := 0
	for _, root := range roots {
		found, bytesRead, err := discoverRoot(root)
		if err != nil {
			return Catalog{}, err
		}
		total += bytesRead
		if total > maxSkillTotalBytes {
			return Catalog{}, fmt.Errorf("skills: catalog exceeds %d-byte limit", maxSkillTotalBytes)
		}
		for _, skill := range found {
			if previous, exists := selected[skill.Name]; exists {
				conflicts = append(conflicts, Conflict{
					Name: skill.Name, WinnerSource: skill.Source, IgnoredSource: previous.Source,
				})
			}
			selected[skill.Name] = skill
		}
	}
	if len(selected) > maxSkills {
		return Catalog{}, fmt.Errorf("skills: catalog contains %d skills, limit is %d", len(selected), maxSkills)
	}
	skills := make([]Skill, 0, len(selected))
	for _, skill := range selected {
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Name < conflicts[j].Name })
	return Catalog{Skills: skills, Conflicts: conflicts}, nil
}

type skillRoot struct {
	scope Scope
	path  string
	label string
}

func discoverRoot(root skillRoot) ([]Skill, int, error) {
	entries, err := os.ReadDir(root.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("skills: read %s: %w", root.label, err)
	}
	canonicalRoot, err := canonicalDirectory(root.path)
	if err != nil {
		return nil, 0, fmt.Errorf("skills: resolve %s: %w", root.label, err)
	}
	var result []Skill
	total := 0
	for _, entry := range entries {
		name := entry.Name()
		if !validName(name) {
			continue
		}
		directory := filepath.Join(root.path, name)
		info, err := os.Stat(directory)
		if err != nil {
			return nil, 0, fmt.Errorf("skills: stat %s/%s: %w", root.label, name, err)
		}
		if !info.IsDir() {
			continue
		}
		path := filepath.Join(directory, "SKILL.md")
		resolved, err := filepath.EvalSymlinks(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, 0, fmt.Errorf("skills: resolve %s/%s/SKILL.md: %w", root.label, name, err)
		}
		if !within(canonicalRoot, resolved) {
			return nil, 0, fmt.Errorf("skills: %s/%s/SKILL.md escapes its skill root", root.label, name)
		}
		content, digest, err := readSkillFile(resolved)
		if err != nil {
			return nil, 0, fmt.Errorf("skills: %s/%s/SKILL.md: %w", root.label, name, err)
		}
		metadata, err := parseFrontmatter(content)
		if err != nil {
			return nil, 0, fmt.Errorf("skills: %s/%s/SKILL.md: %w", root.label, name, err)
		}
		if metadata.name != name {
			return nil, 0, fmt.Errorf("skills: %s/%s/SKILL.md declares name %q", root.label, name, metadata.name)
		}
		total += len(content)
		result = append(result, Skill{
			Name: name, Description: metadata.description,
			Source: root.label + "/" + name + "/SKILL.md", Scope: root.scope,
			Path: resolved, SHA256: digest, Size: len(content),
		})
	}
	return result, total, nil
}

type frontmatter struct {
	name        string
	description string
}

func parseFrontmatter(content string) (frontmatter, error) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 1024), maxSkillFileBytes)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return frontmatter{}, errors.New("frontmatter must start with ---")
	}
	values := make(map[string]string)
	closed := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "---" {
			closed = true
			break
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return frontmatter{}, fmt.Errorf("invalid frontmatter line %q", line)
		}
		key = strings.TrimSpace(key)
		if key != "name" && key != "description" {
			continue
		}
		if _, duplicate := values[key]; duplicate {
			return frontmatter{}, fmt.Errorf("duplicate %s field", key)
		}
		decoded, err := decodeScalar(strings.TrimSpace(value))
		if err != nil {
			return frontmatter{}, fmt.Errorf("invalid %s: %w", key, err)
		}
		values[key] = decoded
	}
	if err := scanner.Err(); err != nil {
		return frontmatter{}, err
	}
	if !closed {
		return frontmatter{}, errors.New("frontmatter is not closed")
	}
	name := strings.TrimSpace(values["name"])
	description := strings.TrimSpace(values["description"])
	if !validName(name) {
		return frontmatter{}, errors.New("name must match [a-z0-9][a-z0-9_-]{0,63}")
	}
	if description == "" || len(description) > maxDescriptionBytes || strings.ContainsAny(description, "\r\n") {
		return frontmatter{}, fmt.Errorf("description must be one non-empty line up to %d bytes", maxDescriptionBytes)
	}
	return frontmatter{name: name, description: description}, nil
}

func decodeScalar(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "\"") {
		return strconv.Unquote(value)
	}
	if strings.HasPrefix(value, "'") {
		if len(value) < 2 || !strings.HasSuffix(value, "'") {
			return "", errors.New("unterminated quoted value")
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), nil
	}
	return value, nil
}

func readSkillFile(path string) (content string, digest string, resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer func() {
		if err := file.Close(); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", errors.New("not a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSkillFileBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(raw) > maxSkillFileBytes {
		return "", "", fmt.Errorf("exceeds %d-byte limit", maxSkillFileBytes)
	}
	if !utf8.Valid(raw) {
		return "", "", errors.New("not UTF-8 text")
	}
	sum := sha256.Sum256(raw)
	return string(raw), "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for index, value := range []byte(name) {
		if value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || index > 0 && (value == '-' || value == '_') {
			continue
		}
		return false
	}
	return true
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
		return "", errors.New("not a directory")
	}
	return filepath.EvalSymlinks(absolute)
}

func within(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
