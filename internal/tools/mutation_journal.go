// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"io/fs"
)

var (
	// ErrMutationJournalMissing prevents a write tool from mutating project
	// files when the durable write-intent boundary was not wired.
	ErrMutationJournalMissing = errors.New("mutation journal is required")
	// ErrMutationConflict means the current file no longer matches the
	// state that a Journal operation expected. Callers must never overwrite it.
	ErrMutationConflict = errors.New("mutation journal conflict")
)

// MutationIntent is the bounded fact a write tool knows immediately before
// it mutates one project file. File contents remain outside this protocol.
type MutationIntent struct {
	ToolCallID string
	ToolName   string
	CallDigest string
	Path       string
	PreExists  bool
	PreSHA256  string
	PreMode    fs.FileMode
	PostExists bool
	PostSHA256 string
	PostMode   fs.FileMode
}

// MutationReceipt is an opaque durable Journal identity. It carries no
// authority and cannot replace a one-shot Capability.
type MutationReceipt struct {
	JournalID string
}

// MutationJournal is defined by the tool-side consumer. Implementations must
// commit Prepare before it returns, and VerifyPost must compare the current
// project file with the persisted post-state before marking it verified.
type MutationJournal interface {
	Prepare(context.Context, MutationIntent) (MutationReceipt, error)
	MarkApplied(context.Context, MutationReceipt) error
	VerifyPost(context.Context, MutationReceipt) error
}
