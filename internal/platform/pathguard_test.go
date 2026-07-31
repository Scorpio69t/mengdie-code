// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newGuard(t *testing.T, root string) *PathGuard {
	t.Helper()
	guard, err := NewPathGuard(root)
	if err != nil {
		t.Fatalf("NewPathGuard() error = %v", err)
	}
	return guard
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveRelativePathInsideRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "internal", "main.go"), "package main")
	guard := newGuard(t, root)

	resolved, err := guard.Resolve(filepath.Join("internal", "main.go"), AccessRead)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Sensitive {
		t.Fatal("ordinary source file marked sensitive")
	}
	if !strings.HasSuffix(resolved.Path, filepath.Join("internal", "main.go")) {
		t.Fatalf("resolved path = %q", resolved.Path)
	}
}

func TestResolveRejectsEscapes(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	evil := filepath.Join(parent, "project-evil")
	writeFile(t, filepath.Join(root, "ok.txt"), "ok")
	writeFile(t, filepath.Join(evil, "secret.txt"), "secret")
	guard := newGuard(t, root)

	for name, path := range map[string]string{
		"dotdot escape":         filepath.Join("..", "project-evil", "secret.txt"),
		"deep dotdot":           filepath.Join("..", "..", "etc", "passwd"),
		"absolute outside":      evil,
		"absolute sibling":      filepath.Join(evil, "secret.txt"),
		"prefix bypass sibling": filepath.Join("..", "project-evil"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := guard.Resolve(path, AccessRead)
			if !errors.Is(err, ErrOutsideRoot) {
				t.Fatalf("Resolve(%q) error = %v, want ErrOutsideRoot", path, err)
			}
		})
	}
}

func TestResolveAllowsNewFileInsideRoot(t *testing.T) {
	root := t.TempDir()
	guard := newGuard(t, root)

	resolved, err := guard.Resolve(filepath.Join("new", "nested", "file.txt"), AccessWrite)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Sensitive {
		t.Fatal("new ordinary file marked sensitive")
	}
}

func TestResolveRejectsEmptyAndNUL(t *testing.T) {
	guard := newGuard(t, t.TempDir())
	for _, path := range []string{"", "   ", "a\x00b"} {
		if _, err := guard.Resolve(path, AccessRead); err == nil {
			t.Fatalf("Resolve(%q) succeeded", path)
		}
	}
}

func TestProtectedPaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "config"), "[core]")
	writeFile(t, filepath.Join(root, ".env"), "TOKEN=x")
	writeFile(t, filepath.Join(root, "certs", "server.pem"), "PEM")
	writeFile(t, filepath.Join(root, ".mengdie", "config.toml"), "")
	guard := newGuard(t, root)

	for _, path := range []string{
		filepath.Join(".git", "config"),
		".env",
		filepath.Join("certs", "server.pem"),
		filepath.Join(".mengdie", "config.toml"),
	} {
		t.Run(path, func(t *testing.T) {
			read, err := guard.Resolve(path, AccessRead)
			if err != nil {
				t.Fatalf("read Resolve() error = %v", err)
			}
			if !read.Sensitive {
				t.Fatal("protected path not marked sensitive")
			}
			if _, err := guard.Resolve(path, AccessWrite); !errors.Is(err, ErrProtectedWrite) {
				t.Fatalf("write Resolve() error = %v, want ErrProtectedWrite", err)
			}
		})
	}
}

func TestResolveFollowsSymlinkEscapes(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	outside := filepath.Join(parent, "outside")
	writeFile(t, filepath.Join(root, "ok.txt"), "ok")
	writeFile(t, filepath.Join(outside, "secret.txt"), "secret")
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink on this host: %v", err)
	}
	guard := newGuard(t, root)

	for name, path := range map[string]string{
		"read through symlink":      filepath.Join("link", "secret.txt"),
		"new file through symlink":  filepath.Join("link", "new.txt"),
		"symlink root substitution": "link",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := guard.Resolve(path, AccessRead); !errors.Is(err, ErrOutsideRoot) {
				t.Fatalf("Resolve(%q) error = %v, want ErrOutsideRoot", path, err)
			}
		})
	}
}

func TestResolveFollowsSymlinkInsideRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real", "file.txt"), "content")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Skipf("cannot create symlink on this host: %v", err)
	}
	guard := newGuard(t, root)

	resolved, err := guard.Resolve(filepath.Join("alias", "file.txt"), AccessRead)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !strings.HasSuffix(resolved.Path, filepath.Join("real", "file.txt")) {
		t.Fatalf("resolved path = %q, want real file path", resolved.Path)
	}
}

func TestResolveMarksSensitiveSymlinkName(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real", "file.txt"), "content")
	// A symlink named ".git" pointing inside the root: the resolved path
	// hides the sensitive component, so the pre-resolution path must be
	// checked as well.
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, ".git")); err != nil {
		t.Skipf("cannot create symlink on this host: %v", err)
	}
	guard := newGuard(t, root)

	read, err := guard.Resolve(filepath.Join(".git", "file.txt"), AccessRead)
	if err != nil {
		t.Fatalf("read Resolve() error = %v", err)
	}
	if !read.Sensitive {
		t.Fatal("access through .git symlink not marked sensitive")
	}
	if _, err := guard.Resolve(filepath.Join(".git", "file.txt"), AccessWrite); !errors.Is(err, ErrProtectedWrite) {
		t.Fatalf("write Resolve() error = %v, want ErrProtectedWrite", err)
	}
}

func TestWindowsFlavorSyntax(t *testing.T) {
	// Pure syntax checks are host-independent; they must work on every CI OS.
	for name, test := range map[string]struct {
		path string
		want error
	}{
		"extended length":  {`\\?\D:\project\file`, ErrDevicePath},
		"device namespace": {`\\.\PhysicalDrive0`, ErrDevicePath},
		"unc":              {`\\server\share\file`, ErrUNCPath},
		"unc slashes":      {`//server/share/file`, ErrUNCPath},
		"drive relative":   {`C:temp\file`, ErrDriveRelative},
		"rooted relative":  {`\windows\system32`, ErrDriveRelative},
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkWindowsSyntax(test.path); !errors.Is(err, test.want) {
				t.Fatalf("checkWindowsSyntax(%q) error = %v, want %v", test.path, err, test.want)
			}
		})
	}
}

func TestWindowsFlavorComponents(t *testing.T) {
	for name, test := range map[string]struct {
		path string
		want error
	}{
		"ads":                     {`D:\project\file.txt:secret`, ErrADS},
		"ads nested":              {`D:\project\dir\file:stream:$DATA`, ErrADS},
		"reserved NUL":            {`D:\project\NUL`, ErrDevicePath},
		"reserved with extension": {`D:\project\con.txt`, ErrDevicePath},
		"reserved lowercase":      {`d:\project\com1`, ErrDevicePath},
		"reserved trailing space": {`D:\project\NUL `, ErrDevicePath},
		"reserved trailing dot":   {`D:\project\con.`, ErrDevicePath},
		"reserved dot and space":  {`D:\project\NUL. `, ErrDevicePath},
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkWindowsComponents(test.path); !errors.Is(err, test.want) {
				t.Fatalf("checkWindowsComponents(%q) error = %v, want %v", test.path, err, test.want)
			}
		})
	}

	for _, path := range []string{
		`D:\project\internal\main.go`,
		`d:\PROJECT\file.txt`,
		`C:\project\nullable.go`,
		`C:\project\console.go`,
		`C:\project\ordinary. `,
	} {
		if err := checkWindowsComponents(path); err != nil {
			t.Fatalf("checkWindowsComponents(%q) error = %v", path, err)
		}
	}
}

func TestWindowsFlavorIntegration(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-flavor resolution requires a Windows host")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "file.txt"), "content")
	guard, err := newPathGuard(root, flavorWindows)
	if err != nil {
		t.Fatalf("newPathGuard() error = %v", err)
	}

	resolved, err := guard.Resolve("file.txt", AccessRead)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Sensitive {
		t.Fatal("ordinary file marked sensitive")
	}

	// Case-insensitive containment: same file, mangled drive case.
	mangled := strings.ToUpper(root[:1]) + root[1:]
	if _, err := guard.Resolve(filepath.Join(mangled, "file.txt"), AccessRead); err != nil {
		t.Fatalf("Resolve() with mangled drive case error = %v", err)
	}

	if _, err := guard.Resolve(`\\?\D:\evil`, AccessRead); !errors.Is(err, ErrDevicePath) {
		t.Fatalf("Resolve() error = %v, want ErrDevicePath", err)
	}
	if _, err := guard.Resolve(`\\server\share`, AccessRead); !errors.Is(err, ErrUNCPath) {
		t.Fatalf("Resolve() error = %v, want ErrUNCPath", err)
	}
}

func TestWindowsContainmentIsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("filepath.Rel separators are host-specific")
	}
	guard := &PathGuard{root: `D:\Source\MengDie`, flavor: flavorWindows}
	for candidate, want := range map[string]bool{
		`d:\source\mengdie\file.txt`:     true,
		`D:\SOURCE\MENGDIE\a\b\c.go`:     true,
		`d:\source\mengdie-evil\file.go`: false,
		`D:\Source\other\file.go`:        false,
	} {
		if got := guard.withinRoot(candidate); got != want {
			t.Errorf("withinRoot(%q) = %v, want %v", candidate, got, want)
		}
	}
}
