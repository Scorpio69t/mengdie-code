// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"

	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// EventReader returns the events for a single session, oldest first,
// capped at `limit`. It is the read-side hook that lets deterministic
// (Rules) and LLM-driven (LLM) extractors share one event source without
// depending on the SQLite schema directly.
//
// The interface lives in the extractor package (not in session) so that
// internal/session does not depend on internal/memory/extractor and
// therefore does not pull in memory, agent, or app transitively. The
// production adapter is NewSQLiteReader below; tests inject their own
// fake via NewRules(NewRules(reader)).
type EventReader interface {
	Events(ctx context.Context, sessionID string, limit int) ([]session.EventRow, error)
}

// NewSQLiteReader returns an EventReader backed by *session.SQLiteStore.
// The adapter exists in extractor (rather than as a method on
// SQLiteStore) to keep the import direction one-way:
// internal/memory/extractor -> internal/session.
func NewSQLiteReader(store *session.SQLiteStore) EventReader {
	return sqliteReader{store: store}
}

type sqliteReader struct {
	store *session.SQLiteStore
}

func (r sqliteReader) Events(ctx context.Context, sessionID string, limit int) ([]session.EventRow, error) {
	if r.store == nil {
		return nil, nil
	}
	return r.store.Events(ctx, sessionID, limit)
}
