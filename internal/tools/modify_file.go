// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxEditableFileBytes = 1 << 20
	maxEditTextBytes     = 24 << 10
	maxWriteContentBytes = 32 << 10
)

func readEditableFile(path, toolName string) ([]byte, fs.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: stat: %w", toolName, err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s: %q is not a regular file", toolName, path)
	}
	if info.Size() > maxEditableFileBytes {
		return nil, 0, fmt.Errorf("%s: file exceeds %d-byte edit limit", toolName, maxEditableFileBytes)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: read: %w", toolName, err)
	}
	if err := validateText(content, toolName, "file"); err != nil {
		return nil, 0, err
	}
	return content, info.Mode(), nil
}

func validateText(content []byte, toolName, field string) error {
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return fmt.Errorf("%s: %s must be UTF-8 text without NUL bytes", toolName, field)
	}
	return nil
}

func bytesSHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func exactEditDiff(path, oldText, newText string, replacements int) (string, error) {
	var preview strings.Builder
	preview.WriteString("--- ")
	preview.WriteString(path)
	preview.WriteString("\n+++ ")
	preview.WriteString(path)
	preview.WriteByte('\n')
	for index := range replacements {
		preview.WriteString(fmt.Sprintf("@@ exact replacement %d/%d @@\n", index+1, replacements))
		appendDiffText(&preview, '-', oldText)
		appendDiffText(&preview, '+', newText)
	}
	if preview.Len() > DefaultToolOutputBytes {
		return "", fmt.Errorf("edit_file: diff preview exceeds %d bytes", DefaultToolOutputBytes)
	}
	return preview.String(), nil
}

func fullFileDiff(path string, before, after []byte, creating bool) (string, error) {
	var preview strings.Builder
	if creating {
		preview.WriteString("--- /dev/null\n")
	} else {
		preview.WriteString("--- ")
		preview.WriteString(path)
		preview.WriteByte('\n')
	}
	preview.WriteString("+++ ")
	preview.WriteString(path)
	preview.WriteByte('\n')
	if creating {
		preview.WriteString("@@ create file @@\n")
	} else {
		preview.WriteString("@@ replace complete file @@\n")
		appendDiffText(&preview, '-', string(before))
	}
	appendDiffText(&preview, '+', string(after))
	if preview.Len() > DefaultToolOutputBytes {
		return "", fmt.Errorf("write_file: diff preview exceeds %d bytes; use edit_file for a focused change", DefaultToolOutputBytes)
	}
	return preview.String(), nil
}

func appendDiffText(builder *strings.Builder, prefix byte, text string) {
	if text == "" {
		return
	}
	for len(text) > 0 {
		builder.WriteByte(prefix)
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			builder.WriteString(text[:newline+1])
			text = text[newline+1:]
			continue
		}
		builder.WriteString(text)
		builder.WriteByte('\n')
		break
	}
}

func ensureSamePreparedPath(call *PreparedCall, resolved string) error {
	if len(call.Paths) != 1 || call.Paths[0].Path != resolved {
		return &PreconditionError{Path: resolved, Reason: "resolved path changed after approval"}
	}
	return nil
}

func atomicWriteFile(rootDir, path string, content []byte, mode fs.FileMode, replace bool, preconditions []Precondition, beforeMutation func() error) (err error) {
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return fmt.Errorf("open project root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	targetPath, err := relativeRootPath(rootDir, path)
	if err != nil {
		return err
	}
	if err := checkRootedPreconditions(rootDir, root, preconditions); err != nil {
		return err
	}
	if beforeMutation != nil {
		if err := beforeMutation(); err != nil {
			return fmt.Errorf("persist write intent: %w", err)
		}
	}
	// Journal persistence is an external scheduling point. Re-check the
	// approved state before creating directories or staging files.
	if err := checkRootedPreconditions(rootDir, root, preconditions); err != nil {
		return err
	}
	createdDirs, err := createParentDirectories(root, filepath.Dir(targetPath))
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		err = errors.Join(err, removeCreatedDirectories(root, createdDirs))
	}()

	temporary, temporaryPath, err := createRootedStagingFile(root, filepath.Dir(targetPath))
	if err != nil {
		return fmt.Errorf("create staging file: %w", err)
	}
	temporaryOpen := true
	defer func() {
		if temporaryOpen {
			err = errors.Join(err, temporary.Close())
		}
		if removeErr := root.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove staging file: %w", removeErr))
		}
	}()

	if err := temporary.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("set staging file permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write staging file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync staging file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		temporaryOpen = false
		return fmt.Errorf("close staging file: %w", err)
	}
	temporaryOpen = false

	if err := checkRootedPreconditions(rootDir, root, preconditions); err != nil {
		return err
	}
	if replace {
		if err := root.Rename(temporaryPath, targetPath); err != nil {
			return fmt.Errorf("replace target atomically: %w", err)
		}
		committed = true
		return nil
	}
	if err := root.Link(temporaryPath, targetPath); err != nil {
		return fmt.Errorf("create target atomically: %w", err)
	}
	committed = true
	if err := root.Remove(temporaryPath); err != nil {
		return fmt.Errorf("remove staging link after commit: %w", err)
	}
	return nil
}

func relativeRootPath(rootDir, path string) (string, error) {
	relative, err := filepath.Rel(rootDir, path)
	if err != nil {
		return "", fmt.Errorf("make path relative to project root: %w", err)
	}
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is not a file inside project root", path)
	}
	return relative, nil
}

func createRootedStagingFile(root *os.Root, dir string) (*os.File, string, error) {
	for range 100 {
		name := filepath.Join(dir, ".mengdie-write-"+rand.Text())
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("could not allocate a unique staging file")
}

func checkRootedPreconditions(rootDir string, root *os.Root, preconditions []Precondition) error {
	for _, precondition := range preconditions {
		relative, err := relativeRootPath(rootDir, precondition.Path)
		if err != nil {
			return &PreconditionError{Path: precondition.Path, Reason: err.Error()}
		}
		switch precondition.Kind {
		case PreconditionFileSHA256:
			file, err := root.Open(relative)
			if err != nil {
				return &PreconditionError{Path: precondition.Path, Reason: err.Error()}
			}
			sum := sha256.New()
			_, copyErr := file.WriteTo(sum)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return &PreconditionError{Path: precondition.Path, Reason: err.Error()}
			}
			if hex.EncodeToString(sum.Sum(nil)) != precondition.SHA256 {
				return &PreconditionError{Path: precondition.Path, Reason: "content changed after approval"}
			}
		case PreconditionPathAbsent:
			_, err := root.Lstat(relative)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return &PreconditionError{Path: precondition.Path, Reason: err.Error()}
			}
			return &PreconditionError{Path: precondition.Path, Reason: "path now exists"}
		default:
			return fmt.Errorf("tools: unknown precondition kind %q", precondition.Kind)
		}
	}
	return nil
}

func createParentDirectories(root *os.Root, dir string) ([]string, error) {
	var missing []string
	for current := dir; ; current = filepath.Dir(current) {
		if current == "." {
			break
		}
		info, err := root.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return nil, fmt.Errorf("parent path %q is not a directory", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect parent directory %q: %w", current, err)
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	slices.Reverse(missing)
	created := make([]string, 0, len(missing))
	for _, path := range missing {
		if err := root.Mkdir(path, 0o755); err != nil {
			if errors.Is(err, fs.ErrExist) {
				info, statErr := root.Stat(path)
				if statErr == nil && info.IsDir() {
					continue
				}
			}
			return nil, errors.Join(
				fmt.Errorf("create parent directory %q: %w", path, err),
				removeCreatedDirectories(root, created),
			)
		}
		created = append(created, path)
	}
	return created, nil
}

func removeCreatedDirectories(root *os.Root, created []string) error {
	var cleanupErr error
	for index := len(created) - 1; index >= 0; index-- {
		path := created[index]
		if err := root.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove created directory %q: %w", path, err))
		}
	}
	return cleanupErr
}
