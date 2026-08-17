// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/session"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func TestRewindCommandRequiresInteractiveApprovalAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	application, stdout, _ := newTestApp(t, nil)
	path := filepath.Join(root, "value.txt")
	journalID := seedAppRewindJournal(t, application, root, path, "before\n", "after\n")

	if code := application.Run(context.Background(), []string{"rewind", "--cwd", root, "session-rewind"}, false); code != ExitPolicyDenied {
		t.Fatalf("non-interactive code=%d want=%d", code, ExitPolicyDenied)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "after\n" {
		t.Fatalf("non-interactive content=%q err=%v", content, err)
	}

	application.stdin = strings.NewReader("y\n")
	if code := application.Run(context.Background(), []string{"rewind", "--cwd", root, "session-rewind"}, true); code != ExitOK {
		t.Fatalf("interactive code=%d stdout=%q", code, stdout.String())
	}
	content, err = os.ReadFile(path)
	if err != nil || string(content) != "before\n" {
		t.Fatalf("rewound content=%q err=%v", content, err)
	}
	for _, want := range []string{"高风险操作", "-after", "+before", journalID, "已安全撤销"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing %q: %s", want, stdout.String())
		}
	}

	stdout.Reset()
	application.stdin = strings.NewReader("")
	code := application.Run(context.Background(), []string{
		"rewind", "--cwd", root, "--journal-id", journalID, "--command-id", "rewind_run-test", "session-rewind",
	}, true)
	if code != ExitOK || !strings.Contains(stdout.String(), "未重复执行") {
		t.Fatalf("idempotent code=%d stdout=%q", code, stdout.String())
	}
}

func seedAppRewindJournal(t *testing.T, application *App, root, path, before, after string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir: application.dataDir, ProjectRoot: root, Now: application.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestSessionStore(t, store)
	payload, err := session.TaskCommandPayload("seed rewind")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginCommandRun(context.Background(), session.CommandRunMetadata{
		SessionID: "session-rewind", CommandID: "command-rewind", CommandKind: session.CommandKindExec,
		CommandPayload: payload, RunID: "run-rewind", ProjectRoot: root,
		Provider: "test", Model: "test", StartedAt: application.now(),
	}); err != nil {
		t.Fatal(err)
	}
	recorder, err := store.NewPatchJournalRecorder(context.Background(), "session-rewind", "run-rewind", "command-rewind", root)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := recorder.Prepare(context.Background(), tools.MutationIntent{
		ToolCallID: "call-rewind", ToolName: "write_file", CallDigest: strings.Repeat("a", 64), Path: path,
		PreExists: true, PreContent: []byte(before), PreSHA256: appBytesDigest(before), PreMode: info.Mode().Perm(),
		PostExists: true, PostSHA256: appBytesDigest(after), PostMode: info.Mode().Perm(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(after), info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := recorder.MarkApplied(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	if err := recorder.VerifyPost(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	return receipt.JournalID
}

func appBytesDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
