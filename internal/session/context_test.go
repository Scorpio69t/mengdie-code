// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

func TestContextRecorderRoundTripAndOptimisticConflict(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginContextCommand(t, store)
	first, err := store.NewContextRecorder(context.Background(), "session-context", "run-context", "command-context")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.NewContextRecorder(context.Background(), "session-context", "run-context", "command-context")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.RecordMessage(context.Background(), provider.Message{Role: provider.RoleUser, Content: "私有任务"}, true); err != nil {
		t.Fatal(err)
	}
	if err := first.RecordMessage(context.Background(), provider.Message{
		Role:      provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{ID: "call-1", Type: "function", Name: "shell", Arguments: []byte(`{"command":"secret"}`)}},
	}, true); err != nil {
		t.Fatal(err)
	}
	if err := first.RecordMessage(context.Background(), provider.Message{
		Role: provider.RoleTool, ToolCallID: "call-1", Name: "shell", Content: `{"success":true,"output":"已省略"}`,
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := stale.RecordMessage(context.Background(), provider.Message{Role: provider.RoleUser, Content: "stale"}, true); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("stale RecordMessage=%v", err)
	}
	loaded, err := store.LoadContext(context.Background(), "session-context")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 3 || loaded[0].Message.Content != "私有任务" || loaded[2].Completeness != ContextSanitized {
		t.Fatalf("loaded=%+v", loaded)
	}
	var artifactCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifacts`).Scan(&artifactCount); err != nil {
		t.Fatal(err)
	}
	if artifactCount != 0 {
		t.Fatalf("small context unexpectedly created %d artifacts", artifactCount)
	}
	loaded[1].Message.ToolCalls[0].Arguments[0] = 'x'
	again, err := store.LoadContext(context.Background(), "session-context")
	if err != nil || string(again[1].Message.ToolCalls[0].Arguments) != `{"command":"secret"}` {
		t.Fatalf("defensive load=%s err=%v", again[1].Message.ToolCalls[0].Arguments, err)
	}
}

func TestContextRecorderAtRejectsLateWriterPosition(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginContextCommand(t, store)
	first, err := store.NewContextRecorder(context.Background(), "session-context", "run-context", "command-context")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.RecordMessage(context.Background(), provider.Message{Role: provider.RoleUser, Content: "late"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewContextRecorderAt(context.Background(), "session-context", "run-context", "command-context", 0); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("NewContextRecorderAt() error=%v", err)
	}
}

func TestLoadContextRejectsCorruptionAndDeleteCascades(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginContextCommand(t, store)
	recorder, err := store.NewContextRecorder(context.Background(), "session-context", "run-context", "command-context")
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessage(context.Background(), provider.Message{Role: provider.RoleUser, Content: "task"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE context_messages SET message_sha256='sha256:0000000000000000000000000000000000000000000000000000000000000000' WHERE session_id='session-context'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadContext(context.Background(), "session-context"); !errors.Is(err, ErrContextCorrupt) {
		t.Fatalf("LoadContext(corrupt)=%v", err)
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), "session-context"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM context_messages`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("context count=%d", count)
	}
}

func beginContextCommand(t *testing.T, store *SQLiteStore) {
	t.Helper()
	payload, err := TaskCommandPayload("task")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "session-context", CommandID: "command-context", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "run-context", ProjectRoot: filepath.Clean(t.TempDir()),
		Provider: "openai-compatible", Model: "test-model", StartedAt: storeTestTime,
	})
	if err != nil {
		t.Fatal(err)
	}
}
