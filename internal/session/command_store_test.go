// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestBeginCommandRunIsIdempotentAndKeepsIndependentIdentities(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	payload, err := TaskCommandPayload("修复测试")
	if err != nil {
		t.Fatal(err)
	}
	metadata := CommandRunMetadata{
		SessionID: "session-command", CommandID: "command-1", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "run-command", ProjectRoot: filepath.Clean(t.TempDir()),
		Provider: "openai-compatible", Model: "test-model", StartedAt: storeTestTime,
	}
	first, err := store.BeginCommandRun(context.Background(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	if first.Existing || first.Command.ID != "command-1" || first.Command.SessionID != "session-command" || first.RunID != "run-command" {
		t.Fatalf("first=%+v", first)
	}
	second, err := store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "ignored-session", CommandID: "command-1", CommandKind: CommandKindExec,
		CommandPayload: json.RawMessage(" { \"task\" : \"修复测试\" } "), RunID: "ignored-run",
		ProjectRoot: metadata.ProjectRoot, Provider: metadata.Provider, Model: metadata.Model, StartedAt: storeTestTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Existing || second.Command.SessionID != "session-command" || second.RunID != "run-command" {
		t.Fatalf("second=%+v", second)
	}
	otherProject := metadata
	otherProject.ProjectRoot = filepath.Clean(t.TempDir())
	if _, err := store.BeginCommandRun(context.Background(), otherProject); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("different project=%v", err)
	}
	different, err := TaskCommandPayload("另一个任务")
	if err != nil {
		t.Fatal(err)
	}
	metadata.CommandPayload = different
	if _, err := store.BeginCommandRun(context.Background(), metadata); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("different payload=%v", err)
	}
	var sessions, commands, runs int
	if err := store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM sessions), (SELECT COUNT(*) FROM commands), (SELECT COUNT(*) FROM runs)`).Scan(&sessions, &commands, &runs); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || commands != 1 || runs != 1 {
		t.Fatalf("sessions=%d commands=%d runs=%d", sessions, commands, runs)
	}
}

func TestAppendAdvancesCommandStatusAtomically(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	payload, err := TaskCommandPayload("task")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "session-command", CommandID: "command-1", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "run-command", ProjectRoot: filepath.Clean(t.TempDir()),
		Provider: "openai-compatible", Model: "test-model", StartedAt: storeTestTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := testRecord(1, 1, "evt-start", "run.started")
	started.SessionID, started.RunID, started.CommandID = "session-command", "run-command", "command-1"
	if err := store.Append(context.Background(), "session-command", 0, []Record{started}); err != nil {
		t.Fatal(err)
	}
	var status string
	var resultSeq any
	if err := store.db.QueryRow(`SELECT status, result_seq FROM commands WHERE id='command-1'`).Scan(&status, &resultSeq); err != nil {
		t.Fatal(err)
	}
	if status != string(CommandRunning) || resultSeq != nil {
		t.Fatalf("status=%s result=%v", status, resultSeq)
	}
	terminal := testRecord(2, 2, "evt-done", "run.completed")
	terminal.SessionID, terminal.RunID, terminal.CommandID = "session-command", "run-command", "command-1"
	if err := store.Append(context.Background(), "session-command", 1, []Record{terminal}); err != nil {
		t.Fatal(err)
	}
	var sequence uint64
	if err := store.db.QueryRow(`SELECT status, result_seq FROM commands WHERE id='command-1'`).Scan(&status, &sequence); err != nil {
		t.Fatal(err)
	}
	if status != string(CommandApplied) || sequence != 2 {
		t.Fatalf("status=%s result=%d", status, sequence)
	}
}

func TestCompletedCommandWithDeniedToolIsRejectedWithResultSequence(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	payload, err := TaskCommandPayload("task")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "session-command", CommandID: "command-1", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "run-command", ProjectRoot: filepath.Clean(t.TempDir()),
		Provider: "openai-compatible", Model: "test-model", StartedAt: storeTestTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal := testRecord(1, 1, "evt-denied", "run.completed")
	terminal.SessionID, terminal.RunID, terminal.CommandID = "session-command", "run-command", "command-1"
	terminal.Payload = json.RawMessage(`{"summary":"done","denied_tools":1}`)
	if err := store.Append(context.Background(), "session-command", 0, []Record{terminal}); err != nil {
		t.Fatal(err)
	}
	command, err := store.LookupCommand(context.Background(), "command-1")
	if err != nil {
		t.Fatal(err)
	}
	if command.Status != CommandRejected || command.ResultSeq != 1 {
		t.Fatalf("command=%+v", command)
	}
}

func TestRejectUnstartedCommandClosesMetadataWithoutInventingResult(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	payload, err := TaskCommandPayload("task")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "session-command", CommandID: "command-1", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "run-command", ProjectRoot: filepath.Clean(t.TempDir()),
		Provider: "openai-compatible", Model: "test-model", StartedAt: storeTestTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RejectUnstartedCommand(context.Background(), "command-1"); err != nil {
		t.Fatal(err)
	}
	var commandStatus, runStatus, sessionStatus string
	var resultSeq any
	if err := store.db.QueryRow(`SELECT status, result_seq FROM commands WHERE id='command-1'`).Scan(&commandStatus, &resultSeq); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT status FROM runs WHERE id='run-command'`).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT status FROM sessions WHERE id='session-command'`).Scan(&sessionStatus); err != nil {
		t.Fatal(err)
	}
	if commandStatus != "rejected" || resultSeq != nil || runStatus != "failed" || sessionStatus != "failed" {
		t.Fatalf("command=%s result=%v run=%s session=%s", commandStatus, resultSeq, runStatus, sessionStatus)
	}
}
