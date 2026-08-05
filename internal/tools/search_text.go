// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

const searchTextSchema = `{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "要搜索的字面量文本"},
    "path": {"type": "string", "description": "搜索起始目录，缺省为项目根"},
    "glob": {"type": "string", "description": "文件名过滤，如 *.go"},
    "case_sensitive": {"type": "boolean", "description": "是否大小写敏感，缺省 true"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 500, "description": "最大匹配行数，缺省 50"}
  },
  "required": ["query"],
  "additionalProperties": false
}`

const (
	defaultSearchLimit   = 50
	maxSearchLimit       = 500
	searchCommandTimeout = 30 * time.Second
)

type searchTextArgs struct {
	Query         string `json:"query"`
	Path          string `json:"path"`
	Glob          string `json:"glob"`
	CaseSensitive *bool  `json:"case_sensitive"`
	Limit         int    `json:"limit"`
}

func (a *searchTextArgs) validate() error {
	if a.Query == "" {
		return errors.New("search_text: query is required")
	}
	if a.Limit < 0 || a.Limit > maxSearchLimit {
		return fmt.Errorf("search_text: limit must be between 1 and %d", maxSearchLimit)
	}
	if a.Glob != "" {
		if _, err := path.Match(a.Glob, ""); err != nil {
			return fmt.Errorf("search_text: invalid glob %q: %w", a.Glob, err)
		}
	}
	return nil
}

func (a *searchTextArgs) caseSensitive() bool {
	return a.CaseSensitive == nil || *a.CaseSensitive
}

func (a *searchTextArgs) normalizedLimit() int {
	if a.Limit == 0 {
		return defaultSearchLimit
	}
	return a.Limit
}

type searchMatch struct {
	path string // slash form, relative to the search base
	line int
	text string
}

// NewSearchText builds the search_text tool: literal text search that
// prefers a system rg and falls back to a pure Go walker.
func NewSearchText() Tool { return searchTextTool{} }

type searchTextTool struct {
	// forceFallback disables rg for tests.
	forceFallback bool
}

func (searchTextTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "search_text",
		Description: "在项目内搜索字面量文本，按 path:line 排序输出匹配行；优先使用 rg，不可用时使用内置搜索",
		InputSchema: json.RawMessage(searchTextSchema),
		Effects:     []Effect{EffectRead},
	}
}

func (searchTextTool) Prepare(ctx context.Context, raw json.RawMessage, env PrepareEnv) (*PreparedCall, error) {
	base, err := resolveSearchBase(raw, env.Guard)
	if err != nil {
		return nil, err
	}
	return PrepareCall(env.CallID, "search_text", raw,
		[]Effect{EffectRead},
		[]PathResource{{Path: base.Path, Sensitive: base.Sensitive}},
		Preview{Kind: PreviewRead, Title: base.Path, Body: "搜索文本"},
		nil,
	)
}

func resolveSearchBase(raw json.RawMessage, guard *platform.PathGuard) (platform.ResolvedPath, error) {
	var args searchTextArgs
	if err := decodeArgs(raw, &args); err != nil {
		return platform.ResolvedPath{}, err
	}
	if err := args.validate(); err != nil {
		return platform.ResolvedPath{}, err
	}
	target := args.Path
	if target == "" {
		target = "."
	}
	resolved, err := guard.Resolve(target, platform.AccessRead)
	if err != nil {
		return platform.ResolvedPath{}, err
	}
	info, err := os.Stat(resolved.Path)
	if err != nil {
		return platform.ResolvedPath{}, fmt.Errorf("search_text: %w", err)
	}
	if !info.IsDir() {
		return platform.ResolvedPath{}, fmt.Errorf("search_text: %q is not a directory", target)
	}
	return resolved, nil
}

func (t searchTextTool) Execute(ctx context.Context, call *PreparedCall, cap Capability, env ExecEnv) (*ToolResult, error) {
	if err := CheckCapability(ctx, call, cap, env); err != nil {
		return nil, err
	}
	base, err := resolveSearchBase(call.CanonicalArg, env.Guard)
	if err != nil {
		return nil, err
	}
	var args searchTextArgs
	if err := decodeArgs(call.CanonicalArg, &args); err != nil {
		return nil, err
	}

	engine := "fallback"
	matches, truncatedCount, err := t.search(ctx, base.Path, args)
	if err != nil {
		return nil, err
	}
	if !t.forceFallback {
		if _, lookErr := exec.LookPath("rg"); lookErr == nil {
			engine = "rg"
		}
	}

	// Deterministic order: path, then line number.
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].path != matches[j].path {
			return matches[i].path < matches[j].path
		}
		return matches[i].line < matches[j].line
	})

	baseRel, err := filepath.Rel(env.Guard.Root(), base.Path)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	if len(matches) == 0 {
		b.WriteString("（没有匹配）")
	}
	for _, match := range matches {
		display := match.path
		if baseRel != "." {
			display = filepath.ToSlash(filepath.Join(baseRel, match.path))
		}
		fmt.Fprintf(&b, "%s:%d: %s\n", display, match.line, truncateRunes(match.text, MaxMatchLineLength))
	}
	// §9.3: tool output keeps both head and tail when over budget.
	output, truncatedBytes := truncateHeadTail(strings.TrimRight(b.String(), "\n"), DefaultToolOutputBytes)
	truncated := truncatedCount || truncatedBytes
	if truncatedCount {
		output += fmt.Sprintf("\n… <truncated: limit %d reached>", args.normalizedLimit())
	}
	return &ToolResult{
		Output:    output,
		Truncated: truncated,
		Metadata: map[string]string{
			"path":      base.Path,
			"engine":    engine,
			"matches":   fmt.Sprintf("%d", len(matches)),
			"truncated": fmt.Sprintf("%v", truncated),
		},
	}, nil
}

func (t searchTextTool) search(ctx context.Context, base string, args searchTextArgs) ([]searchMatch, bool, error) {
	if !t.forceFallback {
		if _, err := exec.LookPath("rg"); err == nil {
			return searchWithRG(ctx, base, args)
		}
	}
	return searchWithWalker(ctx, base, args)
}

// searchWithRG runs rg with a fixed argv and no shell. The process runs
// with cwd=base and searches ".", so result paths are relative and never
// contain a drive-letter colon.
func searchWithRG(ctx context.Context, base string, args searchTextArgs) ([]searchMatch, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, searchCommandTimeout)
	defer cancel()

	argv := []string{
		"--line-number", "--no-heading", "--color", "never", "--fixed-strings",
	}
	// rg only applies gitignore rules inside git repositories; enforce the
	// M1 ignore set explicitly so temp projects behave identically. The
	// "**/" prefix anchors the rule at any depth: without it a pattern
	// containing "/" is root-anchored and nested node_modules/build trees
	// would still be searched, diverging from the fallback engine.
	for name := range ignoredDirNames {
		argv = append(argv, "--glob", "!**/"+name+"/**")
	}
	if !args.caseSensitive() {
		argv = append(argv, "--ignore-case")
	}
	if args.Glob != "" {
		argv = append(argv, "--glob", args.Glob)
	}
	argv = append(argv, "--regexp", args.Query, "--", ".")

	cmd := exec.CommandContext(ctx, "rg", argv...)
	cmd.Dir = base
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, false, nil // no matches
		}
		if ctx.Err() != nil {
			return nil, false, fmt.Errorf("search_text: rg timed out: %w", ctx.Err())
		}
		return nil, false, fmt.Errorf("search_text: rg failed: %w", err)
	}

	var matches []searchMatch
	limit := args.normalizedLimit()
	truncated := false
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		match, ok := parseRGLine(scanner.Text())
		if !ok {
			continue
		}
		if len(matches) >= limit {
			truncated = true
			break
		}
		matches = append(matches, match)
	}
	if err := scanner.Err(); err != nil {
		return nil, false, fmt.Errorf("search_text: read rg output: %w", err)
	}
	return matches, truncated, nil
}

// parseRGLine parses "path:line:text" where path is relative (no colon).
func parseRGLine(line string) (searchMatch, bool) {
	first := strings.IndexByte(line, ':')
	if first <= 0 {
		return searchMatch{}, false
	}
	second := strings.IndexByte(line[first+1:], ':')
	if second <= 0 {
		return searchMatch{}, false
	}
	lineNo, err := strconv.Atoi(line[first+1 : first+1+second])
	if err != nil || lineNo < 1 {
		return searchMatch{}, false
	}
	path := strings.TrimPrefix(filepath.ToSlash(line[:first]), "./")
	return searchMatch{path: path, line: lineNo, text: line[first+1+second+1:]}, true
}

// searchWithWalker is the dependency-free engine used when rg is absent.
func searchWithWalker(ctx context.Context, base string, args searchTextArgs) ([]searchMatch, bool, error) {
	needle := args.Query
	fold := !args.caseSensitive()
	if fold {
		needle = strings.ToLower(needle)
	}

	var matches []searchMatch
	truncated := false
	limit := args.normalizedLimit()

	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if path != base && shouldIgnoreDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		if !matchGlob(args.Glob, rel, d.Name()) {
			return nil
		}
		found, err := searchFile(ctx, path, filepath.ToSlash(rel), needle, fold, limit-len(matches))
		if err != nil {
			return err
		}
		matches = append(matches, found...)
		if len(matches) >= limit {
			truncated = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("search_text: walk: %w", err)
	}
	return matches, truncated, nil
}

// searchFile scans one text file for up to budget matches of needle.
func searchFile(ctx context.Context, path, rel, needle string, fold bool, budget int) (matches []searchMatch, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("search_text: open %q: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			matches = nil
			err = errors.Join(err, fmt.Errorf("search_text: close %q: %w", path, closeErr))
		}
	}()

	// Sized to the sniff window so Peek(8<<10) never hits ErrBufferFull.
	reader := bufio.NewReaderSize(file, 8<<10)
	if sniffBinary(reader) {
		return nil, nil
	}

	lineNo := 0
	for len(matches) < budget {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			lineNo++
			text := strings.TrimRight(line, "\r\n")
			haystack := text
			if fold {
				haystack = strings.ToLower(haystack)
			}
			if strings.Contains(haystack, needle) {
				matches = append(matches, searchMatch{path: rel, line: lineNo, text: text})
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("search_text: read %q: %w", path, err)
		}
	}
	return matches, nil
}
