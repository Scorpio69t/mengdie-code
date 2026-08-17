// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// PreconditionError reports a failed precondition: the world changed
// between Prepare/Approval and Execute. Callers must surface this as a
// safe failure, never retry automatically.
type PreconditionError struct {
	Path   string
	Reason string
}

func (e *PreconditionError) Error() string {
	return fmt.Sprintf("precondition failed for %q: %s", e.Path, e.Reason)
}

// FileSHA256 computes the hex SHA-256 of a file. Tools use it during
// Prepare to build preconditions.
func FileSHA256(path string) (digest string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			digest = ""
			err = errors.Join(err, fmt.Errorf("close file after hashing: %w", closeErr))
		}
	}()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// CheckPreconditions verifies every precondition immediately before
// Execute. Any failure is a *PreconditionError.
func CheckPreconditions(preconditions []Precondition) error {
	for _, precondition := range preconditions {
		switch precondition.Kind {
		case PreconditionFileSHA256:
			actual, err := FileSHA256(precondition.Path)
			if errors.Is(err, os.ErrNotExist) {
				return &PreconditionError{Path: precondition.Path, Reason: "file no longer exists"}
			}
			if err != nil {
				return &PreconditionError{Path: precondition.Path, Reason: err.Error()}
			}
			if actual != precondition.SHA256 {
				return &PreconditionError{Path: precondition.Path, Reason: "content changed after approval"}
			}
		case PreconditionFileMode:
			info, err := os.Stat(precondition.Path)
			if errors.Is(err, os.ErrNotExist) {
				return &PreconditionError{Path: precondition.Path, Reason: "file no longer exists"}
			}
			if err != nil {
				return &PreconditionError{Path: precondition.Path, Reason: err.Error()}
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != precondition.Mode.Perm() {
				return &PreconditionError{Path: precondition.Path, Reason: "file mode changed after approval"}
			}
		case PreconditionPathAbsent:
			_, err := os.Lstat(precondition.Path)
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

// CheckFilePreconditions verifies file_sha256 preconditions against an
// already-open file instead of re-opening it by path, closing the window
// in which the target could be swapped between the hash check and the
// read. The file offset is rewound before returning.
func CheckFilePreconditions(preconditions []Precondition, file *os.File) error {
	for _, precondition := range preconditions {
		switch precondition.Kind {
		case PreconditionFileSHA256:
			sum := sha256.New()
			if _, err := io.Copy(sum, file); err != nil {
				return &PreconditionError{Path: precondition.Path, Reason: err.Error()}
			}
			if hex.EncodeToString(sum.Sum(nil)) != precondition.SHA256 {
				return &PreconditionError{Path: precondition.Path, Reason: "content changed after approval"}
			}
		case PreconditionFileMode:
			info, err := file.Stat()
			if err != nil {
				return &PreconditionError{Path: precondition.Path, Reason: err.Error()}
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != precondition.Mode.Perm() {
				return &PreconditionError{Path: precondition.Path, Reason: "file mode changed after approval"}
			}
		default:
			return fmt.Errorf("tools: unknown precondition kind %q", precondition.Kind)
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("tools: rewind after precondition check: %w", err)
	}
	return nil
}
