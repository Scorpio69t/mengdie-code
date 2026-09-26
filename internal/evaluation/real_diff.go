// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	realSnapshotMaxEntries = 100000
	realSnapshotMaxBytes   = 512 << 20
)

type realFileState struct {
	Mode fs.FileMode
	Size int64
	Hash string
}

type realSnapshot map[string]realFileState

// RealChangedFile is redacted evidence: paths and hashes only, no source.
type RealChangedFile struct {
	Path         string `json:"path"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
}

func captureRealSnapshot(root string) (realSnapshot, error) {
	snapshot := make(realSnapshot)
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if len(snapshot) >= realSnapshotMaxEntries {
			return errors.New("real repository file count limit exceeded")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("real repository path escaped snapshot root")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if reparse, err := realPathIsReparse(path); err != nil {
			return err
		} else if reparse {
			return errors.New("real repository contains a reparse point")
		}
		state := realFileState{Mode: info.Mode(), Size: info.Size()}
		switch {
		case info.Mode().IsRegular():
			total += info.Size()
			if info.Size() < 0 || total > realSnapshotMaxBytes {
				return errors.New("real repository snapshot byte limit exceeded")
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			digest := sha256.New()
			read, copyErr := io.CopyN(digest, file, info.Size()+1)
			closeErr := file.Close()
			if copyErr != nil && !errors.Is(copyErr, io.EOF) || closeErr != nil || read != info.Size() {
				return errors.New("real repository snapshot read failed")
			}
			state.Hash = hex.EncodeToString(digest.Sum(nil))
		case info.Mode().IsDir():
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256([]byte(target))
			state.Hash = hex.EncodeToString(digest[:])
		default:
			return errors.New("real repository contains unsupported file type")
		}
		snapshot[filepath.ToSlash(relative)] = state
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot real repository: %w", err)
	}
	return snapshot, nil
}

func allowedRealChanges(before, after realSnapshot, allowlist []string) ([]RealChangedFile, error) {
	allowed := make(map[string]bool, len(allowlist))
	for _, path := range allowlist {
		allowed[path] = true
	}
	paths := make(map[string]bool, len(before)+len(after))
	for path := range before {
		paths[path] = true
	}
	for path := range after {
		paths[path] = true
	}
	var changed []RealChangedFile
	for path := range paths {
		old, oldExists := before[path]
		newState, newExists := after[path]
		if oldExists == newExists && old == newState {
			continue
		}
		if !allowed[path] {
			return nil, errors.New("real repository changed outside exact allowlist")
		}
		if oldExists && !old.Mode.IsRegular() || newExists && !newState.Mode.IsRegular() ||
			oldExists && newExists && old.Mode != newState.Mode {
			return nil, errors.New("real repository changed file type or mode")
		}
		changed = append(changed, RealChangedFile{Path: path, BeforeSHA256: old.Hash, AfterSHA256: newState.Hash})
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Path < changed[j].Path })
	return changed, nil
}

func rebuildRealChanges(agentRoot, verifierRoot string, before, after realSnapshot, changes []RealChangedFile) error {
	for _, change := range changes {
		if err := validateWorkspaceRelativePath(change.Path); err != nil {
			return errors.New("invalid real repository rebuild path")
		}
		old, oldExists := before[change.Path]
		current, currentExists := after[change.Path]
		target := filepath.Join(verifierRoot, filepath.FromSlash(change.Path))
		if err := checkRealPathParents(verifierRoot, target); err != nil {
			return err
		}
		targetInfo, err := os.Lstat(target)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot inspect verifier target")
		}
		if oldExists != (err == nil) || oldExists && (!targetInfo.Mode().IsRegular() || targetInfo.Mode() != old.Mode) {
			return errors.New("verifier source does not match agent source")
		}
		if oldExists {
			hash, err := hashRealRegularFile(target, old.Size)
			if err != nil || hash != old.Hash {
				return errors.New("verifier source hash mismatch")
			}
		}
		if !currentExists {
			if err := os.Remove(target); err != nil {
				return errors.New("cannot remove verified file")
			}
			continue
		}
		source := filepath.Join(agentRoot, filepath.FromSlash(change.Path))
		if err := checkRealPathParents(agentRoot, source); err != nil {
			return err
		}
		info, err := os.Lstat(source)
		if err != nil || !info.Mode().IsRegular() || info.Mode() != current.Mode || info.Size() != current.Size {
			return errors.New("agent file changed during rebuild")
		}
		content, err := os.ReadFile(source)
		if err != nil || int64(len(content)) != current.Size {
			return errors.New("cannot read verified agent file")
		}
		digest := sha256.Sum256(content)
		if hex.EncodeToString(digest[:]) != current.Hash {
			return errors.New("agent file hash changed during rebuild")
		}
		if err := os.WriteFile(target, content, current.Mode.Perm()); err != nil {
			return errors.New("cannot rebuild verified file")
		}
	}
	return nil
}

func checkRealPathParents(root, target string) error {
	parent := filepath.Dir(target)
	for parent != root {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() {
			return errors.New("real repository path parent is not an ordinary directory")
		}
		if reparse, err := realPathIsReparse(parent); err != nil || reparse {
			return errors.New("real repository path parent is a reparse point")
		}
		parent = filepath.Dir(parent)
	}
	return nil
}

func hashRealRegularFile(path string, expectedSize int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	read, err := io.CopyN(digest, file, expectedSize+1)
	if err != nil && !errors.Is(err, io.EOF) || read != expectedSize {
		return "", errors.New("real repository file size changed")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
