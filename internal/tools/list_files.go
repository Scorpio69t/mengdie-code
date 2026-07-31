// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

const listFilesSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "起始目录，缺省为项目根"},
    "glob": {"type": "string", "description": "文件名过滤，如 *.go；含 / 时匹配相对路径"},
    "max_depth": {"type": "integer", "minimum": 1, "description": "相对起始目录的最大递归深度，缺省不限"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 1000, "description": "最大返回条目数，缺省 100"}
  },
  "additionalProperties": false
}`

// ignoredDirNames are skipped by list_files and search_text: VCS/agent
// internals, dependency trees and common build outputs.
var ignoredDirNames = map[string]struct{}{
	"node_modules": {}, "dist": {}, "build": {}, "target": {},
	"out": {}, "bin": {}, "obj": {}, "vendor": {},
}

const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

type listFilesArgs struct {
	Path     string `json:"path"`
	Glob     string `json:"glob"`
	MaxDepth int    `json:"max_depth"`
	Limit    int    `json:"limit"`
}

func (a *listFilesArgs) validate() error {
	if a.MaxDepth < 0 {
		return errors.New("list_files: max_depth must be positive")
	}
	if a.Limit < 0 || a.Limit > maxListLimit {
		return fmt.Errorf("list_files: limit must be between 1 and %d", maxListLimit)
	}
	if a.Glob != "" {
		if _, err := path.Match(a.Glob, ""); err != nil {
			return fmt.Errorf("list_files: invalid glob %q: %w", a.Glob, err)
		}
	}
	return nil
}

// NewListFiles builds the list_files tool: sorted, root-relative directory
// listings with ignore rules, depth and count limits.
func NewListFiles() Tool { return listFilesTool{} }

type listFilesTool struct{}

func (listFilesTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "list_files",
		Description: "列出项目内文件，相对项目根排序输出；默认忽略隐藏项与构建目录",
		InputSchema: json.RawMessage(listFilesSchema),
		Effects:     []Effect{EffectRead},
	}
}

func (t listFilesTool) Prepare(ctx context.Context, raw json.RawMessage, env PrepareEnv) (*PreparedCall, error) {
	base, err := resolveListBase(raw, env.Guard)
	if err != nil {
		return nil, err
	}
	return PrepareCall(env.CallID, "list_files", raw,
		[]Effect{EffectRead},
		Preview{Kind: PreviewRead, Title: base, Body: "列出目录内容"},
		nil,
	)
}

// resolveListBase validates args and resolves the starting directory.
func resolveListBase(raw json.RawMessage, guard *platform.PathGuard) (string, error) {
	var args listFilesArgs
	if err := decodeArgs(raw, &args); err != nil {
		return "", err
	}
	if err := args.validate(); err != nil {
		return "", err
	}
	target := args.Path
	if target == "" {
		target = "."
	}
	resolved, err := guard.Resolve(target, platform.AccessRead)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved.Path)
	if err != nil {
		return "", fmt.Errorf("list_files: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("list_files: %q is not a directory", target)
	}
	return resolved.Path, nil
}

func (listFilesTool) Execute(ctx context.Context, call *PreparedCall, cap Capability, env ExecEnv) (*ToolResult, error) {
	if err := CheckCapability(call, cap); err != nil {
		return nil, err
	}
	base, err := resolveListBase(call.CanonicalArg, env.Guard)
	if err != nil {
		return nil, err
	}
	var args listFilesArgs
	if err := decodeArgs(call.CanonicalArg, &args); err != nil {
		return nil, err
	}
	if args.Limit == 0 {
		args.Limit = defaultListLimit
	}

	entries, truncated, err := walkFiles(ctx, env.Guard.Root(), base, args)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	if len(entries) == 0 {
		b.WriteString("（没有匹配的文件）")
	}
	for _, entry := range entries {
		b.WriteString(entry)
		b.WriteByte('\n')
	}
	if truncated {
		fmt.Fprintf(&b, "… <truncated: limit %d reached>", args.Limit)
	}
	return &ToolResult{
		Output:    strings.TrimRight(b.String(), "\n"),
		Truncated: truncated,
		Metadata: map[string]string{
			"path":      base,
			"entries":   fmt.Sprintf("%d", len(entries)),
			"truncated": fmt.Sprintf("%v", truncated),
		},
	}, nil
}

// walkFiles collects root-relative file paths under base, newest-free
// stable order, honoring ignore rules, depth and glob.
func walkFiles(ctx context.Context, root, base string, args listFilesArgs) ([]string, bool, error) {
	var entries []string
	truncated := false
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		depth := len(strings.Split(rel, string(filepath.Separator)))
		if d.IsDir() {
			if path == base {
				return nil
			}
			if shouldIgnoreDir(d.Name()) || (args.MaxDepth > 0 && depth >= args.MaxDepth) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			// Do not follow symlinked files; the guard resolves real
			// paths when the file is actually read.
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !matchGlob(args.Glob, rel, d.Name()) {
			return nil
		}
		if len(entries) >= args.Limit {
			truncated = true
			return fs.SkipAll
		}
		rootRel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(rootRel))
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("list_files: walk: %w", err)
	}
	sort.Strings(entries)
	return entries, truncated, nil
}

func shouldIgnoreDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	_, ignored := ignoredDirNames[name]
	return ignored
}

// matchGlob matches pattern against the slash form of rel when it contains
// a path separator, otherwise against the basename.
func matchGlob(pattern, rel, base string) bool {
	if pattern == "" {
		return true
	}
	target := base
	if strings.ContainsRune(pattern, '/') {
		target = filepath.ToSlash(rel)
	}
	matched, err := path.Match(pattern, target)
	return err == nil && matched
}
