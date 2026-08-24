// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import "time"

// EventRow is the minimal projection of one durable session event that the
// memory extractor (and future reporting consumers) read. It is intentionally
// narrower than the events table defined in
// internal/session/migrations/001_session_event_store.sql: only the columns
// the spec §4 triggers actually inspect are surfaced, so callers stay
// decoupled from the underlying SQLite schema and payload JSON shape.
//
// SourceRef is a best-effort text carrier: for tool.completed events it is
// taken from the Summary field of events.ToolCompleted (lossy — the actual
// command text lives in the unreplied arguments and is not persisted);
// for run.failed events it carries "category=<RunFailed.Category>".
// Future slices that need richer projection should add a dedicated column
// rather than overload this struct.
type EventRow struct {
	Kind      string    // event kind (e.g. "tool.completed", "run.completed", "run.failed")
	Name      string    // tool name for tool.completed; empty otherwise
	Success   bool      // tool.completed exit status; false for failed runs
	Timestamp time.Time // wall-clock stamp; kept for downstream scoring
	SourceRef string    // shell command summary, run-failure category, etc.
}
