// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package chaos

import (
	"context"

	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

// Journal wraps tools.MutationJournal so the patch-journal pre / post /
// conflict boundaries become chaos hooks. Each hook point carries its own
// intent: pre fires before the durable prepared entry is committed, post
// fires after MarkApplied but before VerifyPost, and conflict fires when
// VerifyPost observes a state mismatch with the recorded post hash.
type Journal struct {
	inner tools.MutationJournal
	ctrl  *Controller
}

// NewJournal wraps a tools.MutationJournal. The inner journal must not be
// nil and must implement all three methods so the wrapper can forward
// every call.
func NewJournal(inner tools.MutationJournal, ctrl *Controller) *Journal {
	if inner == nil {
		panic("chaos: journal inner is nil")
	}
	if ctrl == nil {
		panic("chaos: journal controller is nil")
	}
	return &Journal{inner: inner, ctrl: ctrl}
}

// Prepare fires HookPatchJournalPre before delegating. The recorded
// SideEffect flag is false because Prepare is durable but has not yet
// mutated the project file.
func (j *Journal) Prepare(ctx context.Context, intent tools.MutationIntent) (tools.MutationReceipt, error) {
	decision := j.ctrl.MaybeFire(HookPatchJournalPre, 0, intent.ToolName, false)
	switch decision.Fire {
	case FireAbort:
		return tools.MutationReceipt{}, decision.Err
	case FireContext:
		return tools.MutationReceipt{}, decision.Err
	case FireUnknown:
		return j.inner.Prepare(ctx, intent)
	default:
		return j.inner.Prepare(ctx, intent)
	}
}

// MarkApplied fires HookPatchJournalPost after the wrapped journal records
// the applied transition. SideEffect is true because by this point the
// underlying write tool has already mutated the project file.
func (j *Journal) MarkApplied(ctx context.Context, receipt tools.MutationReceipt) error {
	decision := j.ctrl.MaybeFire(HookPatchJournalPost, 0, receipt.JournalID, true)
	switch decision.Fire {
	case FireAbort:
		return decision.Err
	case FireContext:
		return decision.Err
	case FireUnknown:
		return j.inner.MarkApplied(ctx, receipt)
	default:
		return j.inner.MarkApplied(ctx, receipt)
	}
}

// VerifyPost fires HookPatchJournalConflict when the wrapped journal
// reports a state mismatch (ErrMutationConflict). The wrap never fires the
// conflict hook when VerifyPost succeeds; tests inject conflict by either
// mutating the file between MarkApplied and VerifyPost, or by wrapping a
// stub journal that returns ErrMutationConflict on demand.
func (j *Journal) VerifyPost(ctx context.Context, receipt tools.MutationReceipt) error {
	err := j.inner.VerifyPost(ctx, receipt)
	if err == nil {
		return nil
	}
	if isMutationConflict(err) {
		decision := j.ctrl.MaybeFire(HookPatchJournalConflict, 0, receipt.JournalID, true)
		if decision.Observed {
			return err
		}
	}
	return err
}

func isMutationConflict(err error) bool {
	if err == nil {
		return false
	}
	if err == tools.ErrMutationConflict {
		return true
	}
	// Unwrap to handle wrapped errors that include context.
	for current := err; current != nil; {
		if current == tools.ErrMutationConflict {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		next, ok := current.(unwrapper)
		if !ok {
			return false
		}
		current = next.Unwrap()
	}
	return false
}
