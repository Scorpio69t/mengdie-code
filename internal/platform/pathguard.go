// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package platform isolates operating-system differences behind small,
// testable adapters. P1-04 delivers the path guard; process and shell
// adapters arrive with P1-08.
package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Hard path violations. They are stable sentinels: callers classify with
// errors.Is, the wrapped text carries the offending path for diagnostics.
var (
	ErrEmptyPath      = errors.New("empty path")
	ErrOutsideRoot    = errors.New("path escapes project root")
	ErrProtectedWrite = errors.New("write to protected path")
	ErrUNCPath        = errors.New("UNC paths are not allowed")
	ErrDevicePath     = errors.New("device paths are not allowed")
	ErrADS            = errors.New("alternate data streams are not allowed")
	ErrDriveRelative  = errors.New("drive-relative paths are not supported")
)

// AccessMode distinguishes read from write access. Writes to protected
// locations are hard-denied; reads are flagged Sensitive so that Policy
// (P1-06) can decide between ask and deny.
type AccessMode int

const (
	AccessRead AccessMode = iota
	AccessWrite
)

// pathFlavor selects path semantics independently of the host OS so that
// Windows rules stay testable on macOS/Linux and vice versa.
type pathFlavor int

const (
	flavorUnix pathFlavor = iota
	flavorWindows
)

func hostFlavor() pathFlavor {
	if runtime.GOOS == "windows" {
		return flavorWindows
	}
	return flavorUnix
}

// ResolvedPath is the outcome of a successful guard check.
type ResolvedPath struct {
	// Path is the canonical absolute path with symlinks resolved. It is
	// guaranteed to stay inside the project root.
	Path string
	// Sensitive marks credentials, configuration and VCS internals. Reads
	// are permitted but must be surfaced to Policy; writes never reach this
	// state because they fail with ErrProtectedWrite.
	Sensitive bool
}

// PathGuard enforces the project-root boundary for file tools. It performs
// no I/O beyond existence checks and symlink resolution, and never decides
// approval — it only produces canonical paths and hard violations.
type PathGuard struct {
	root   string
	flavor pathFlavor
}

// NewPathGuard anchors a guard at root using host path semantics. The root
// must exist; it is canonicalized and symlink-resolved once.
func NewPathGuard(root string) (*PathGuard, error) {
	return newPathGuard(root, hostFlavor())
}

func newPathGuard(root string, flavor pathFlavor) (*PathGuard, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("path guard: %w", ErrEmptyPath)
	}
	if flavor == flavorWindows {
		root = stripLongPathPrefix(root)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("path guard: resolve root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("path guard: root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path guard: root %q is not a directory", abs)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("path guard: resolve root symlinks: %w", err)
	}
	if flavor == flavorWindows {
		canonical = stripLongPathPrefix(canonical)
	}
	return &PathGuard{root: filepath.Clean(canonical), flavor: flavor}, nil
}

// Root returns the canonical project root.
func (g *PathGuard) Root() string {
	return g.root
}

// Resolve canonicalizes path and enforces the root boundary. Relative paths
// are anchored at the project root. Existing paths have every symlink,
// junction and reparse point resolved; new paths resolve their nearest
// existing ancestor and are rejoined, so a symlinked parent cannot smuggle
// the file outside the root.
func (g *PathGuard) Resolve(path string, mode AccessMode) (ResolvedPath, error) {
	if strings.TrimSpace(path) == "" {
		return ResolvedPath{}, fmt.Errorf("path guard: %w", ErrEmptyPath)
	}
	if strings.ContainsRune(path, 0) {
		return ResolvedPath{}, fmt.Errorf("path guard: NUL byte in path %q", path)
	}
	if g.flavor == flavorWindows {
		if err := checkWindowsSyntax(path); err != nil {
			return ResolvedPath{}, err
		}
	}

	p := path
	if !g.isAbs(p) {
		p = filepath.Join(g.root, p)
	}
	p = filepath.Clean(p)
	if g.flavor == flavorWindows {
		if err := checkWindowsComponents(p); err != nil {
			return ResolvedPath{}, err
		}
	}

	resolved, err := resolvePath(p)
	if err != nil {
		return ResolvedPath{}, fmt.Errorf("path guard: resolve %q: %w", path, err)
	}
	if g.flavor == flavorWindows {
		resolved = stripLongPathPrefix(resolved)
	}
	resolved = filepath.Clean(resolved)

	if !g.withinRoot(resolved) {
		return ResolvedPath{}, fmt.Errorf("path guard: %w: %q", ErrOutsideRoot, path)
	}

	// Check both the symlink-resolved path and the pre-resolution path: a
	// symlink named e.g. ".git" pointing inside the root must still mark
	// accesses through it as sensitive.
	sensitive := g.isSensitive(resolved) || g.isSensitive(p)
	if sensitive && mode == AccessWrite {
		return ResolvedPath{}, fmt.Errorf("path guard: %w: %q", ErrProtectedWrite, path)
	}
	return ResolvedPath{Path: resolved, Sensitive: sensitive}, nil
}

// isAbs reports whether p is absolute under the guard's flavor, independent
// of the host OS.
func (g *PathGuard) isAbs(p string) bool {
	if g.flavor == flavorWindows {
		return len(p) >= 3 && isDriveLetter(p[0]) && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
	}
	return strings.HasPrefix(p, "/")
}

// withinRoot reports whether the canonical resolved path stays inside the
// root. Comparison uses filepath.Rel with flavor-aware case folding; string
// prefix checks are never sufficient ("project-evil" shares a prefix).
func (g *PathGuard) withinRoot(resolved string) bool {
	root, candidate := g.root, resolved
	if g.flavor == flavorWindows {
		root = strings.ToLower(root)
		candidate = strings.ToLower(candidate)
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// isSensitive marks VCS internals, agent configuration and credential files.
// Matching is case-insensitive on Windows flavor.
func (g *PathGuard) isSensitive(resolved string) bool {
	rel, err := filepath.Rel(g.root, resolved)
	if err != nil {
		return false
	}
	components := strings.Split(rel, string(filepath.Separator))
	for _, component := range components {
		if g.fold(component) == ".git" || g.fold(component) == ".mengdie" ||
			g.fold(component) == ".ssh" || g.fold(component) == ".aws" || g.fold(component) == ".gnupg" {
			return true
		}
	}
	base := g.fold(filepath.Base(resolved))
	if base == ".env" || base == ".envrc" || strings.HasPrefix(base, ".env.") {
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".keystore"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	switch base {
	case "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
		".netrc", ".pgpass", ".npmrc", "credentials":
		return true
	}
	return false
}

func (g *PathGuard) fold(s string) string {
	if g.flavor == flavorWindows {
		return strings.ToLower(s)
	}
	return s
}

// resolvePath returns p with all symlinks resolved. When p does not exist,
// its nearest existing ancestor is resolved and the remaining components are
// rejoined.
func resolvePath(p string) (string, error) {
	if _, err := os.Lstat(p); err == nil {
		return filepath.EvalSymlinks(p)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var rest []string
	dir := p
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no existing ancestor for %q: %w", p, fs.ErrNotExist)
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
		dir = parent
		if _, err := os.Lstat(dir); err == nil {
			base, err := filepath.EvalSymlinks(dir)
			if err != nil {
				return "", err
			}
			return filepath.Join(append([]string{base}, rest...)...), nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
}

// stripLongPathPrefix removes the \\?\ extended-length prefix that
// EvalSymlinks may return on Windows.
func stripLongPathPrefix(p string) string {
	if rest, ok := strings.CutPrefix(p, `\\?\UNC\`); ok {
		return `\\` + rest
	}
	return strings.TrimPrefix(p, `\\?\`)
}
