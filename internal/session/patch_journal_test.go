// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func TestPatchJournalRecoveryClassifiesPrePostAndConflict(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		wantStatus PatchStatus
	}{
		{name: "write not applied", current: "before\n", wantStatus: PatchAborted},
		{name: "write applied before fact", current: "after\n", wantStatus: PatchVerified},
		{name: "external edit", current: "changed elsewhere\n", wantStatus: PatchConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, recorder, root := newPatchJournalHarness(t)
			defer closeTestStore(t, store)
			path := filepath.Join(root, "value.txt")
			if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			receipt, err := recorder.Prepare(context.Background(), patchTestIntent(t, path, true, "before\n", "after\n"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.current), 0o600); err != nil {
				t.Fatal(err)
			}
			facts, err := store.RecoverPatchJournals(context.Background(), "session-patch", root)
			if err != nil {
				t.Fatal(err)
			}
			if len(facts) != 1 || facts[0].JournalID != receipt.JournalID || facts[0].Status != test.wantStatus {
				t.Fatalf("facts=%+v", facts)
			}
			var journalStatus, entryStatus string
			if err := store.db.QueryRow(`
SELECT j.status, e.status FROM patch_journals j JOIN patch_entries e ON e.journal_id=j.id
WHERE j.id=?`, receipt.JournalID).Scan(&journalStatus, &entryStatus); err != nil {
				t.Fatal(err)
			}
			if journalStatus != string(test.wantStatus) || entryStatus != string(test.wantStatus) {
				t.Fatalf("journal=%s entry=%s", journalStatus, entryStatus)
			}
			if test.wantStatus == PatchConflict && facts[0].ConflictMsg == "" {
				t.Fatal("conflict did not preserve a bounded explanation")
			}
		})
	}
}

func TestPatchJournalLiveVerificationAndCreateRecovery(t *testing.T) {
	t.Run("applied write is verified", func(t *testing.T) {
		store, recorder, root := newPatchJournalHarness(t)
		defer closeTestStore(t, store)
		path := filepath.Join(root, "value.txt")
		if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		receipt, err := recorder.Prepare(context.Background(), patchTestIntent(t, path, true, "before\n", "after\n"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := recorder.MarkApplied(context.Background(), receipt); err != nil {
			t.Fatal(err)
		}
		if err := recorder.VerifyPost(context.Background(), receipt); err != nil {
			t.Fatal(err)
		}
		facts, err := store.RecoverPatchJournals(context.Background(), "session-patch", root)
		if err != nil || len(facts) != 1 || facts[0].Status != PatchVerified {
			t.Fatalf("facts=%+v err=%v", facts, err)
		}
	})

	t.Run("missing create remains aborted", func(t *testing.T) {
		store, recorder, root := newPatchJournalHarness(t)
		defer closeTestStore(t, store)
		path := filepath.Join(root, "new.txt")
		intent := patchTestIntent(t, path, false, "", "created\n")
		if _, err := recorder.Prepare(context.Background(), intent); err != nil {
			t.Fatal(err)
		}
		facts, err := store.RecoverPatchJournals(context.Background(), "session-patch", root)
		if err != nil || len(facts) != 1 || facts[0].Status != PatchAborted {
			t.Fatalf("facts=%+v err=%v", facts, err)
		}
	})
}

func TestPatchJournalRejectsUnsafeIdentityAndCascadesWithSession(t *testing.T) {
	store, recorder, root := newPatchJournalHarness(t)
	defer closeTestStore(t, store)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	intent := patchTestIntent(t, outside, false, "", "outside\n")
	if _, err := recorder.Prepare(context.Background(), intent); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("Prepare(outside) error=%v", err)
	}
	path := filepath.Join(root, "value.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Prepare(context.Background(), patchTestIntent(t, path, true, "before\n", "after\n")); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), "session-patch"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"patch_journals", "patch_entries"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows=%d after session delete", table, count)
		}
	}
}

func TestPatchJournalVerifyPostFailsClosedAfterExternalChange(t *testing.T) {
	store, recorder, root := newPatchJournalHarness(t)
	defer closeTestStore(t, store)
	path := filepath.Join(root, "value.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt, err := recorder.Prepare(context.Background(), patchTestIntent(t, path, true, "before\n", "after\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recorder.MarkApplied(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	if err := recorder.VerifyPost(context.Background(), receipt); !errors.Is(err, tools.ErrMutationConflict) {
		t.Fatalf("VerifyPost() error=%v", err)
	}
}

func TestPatchJournalRecorderCanonicalizesEquivalentRootAliases(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	realRoot := t.TempDir()
	aliasRoot := strings.ToUpper(realRoot)
	if runtime.GOOS != "windows" {
		aliasRoot = filepath.Join(t.TempDir(), "project-link")
		if err := os.Symlink(realRoot, aliasRoot); err != nil {
			t.Fatal(err)
		}
	}
	payload, err := TaskCommandPayload("canonical root alias")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "session-alias", CommandID: "command-alias", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "run-alias", ProjectRoot: aliasRoot,
		Provider: "openai-compatible", Model: "test-model", StartedAt: storeTestTime,
	}); err != nil {
		t.Fatal(err)
	}
	recorder, err := store.NewPatchJournalRecorder(
		context.Background(), "session-alias", "run-alias", "command-alias", aliasRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := platform.NewPathGuard(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(guard.Root(), "value.txt")
	if _, err := recorder.Prepare(context.Background(), patchTestIntent(t, path, false, "", "after\n")); err != nil {
		t.Fatal(err)
	}
}

func newPatchJournalHarness(t *testing.T) (*SQLiteStore, *PatchJournalRecorder, string) {
	t.Helper()
	store := openTestStore(t, t.TempDir(), 0)
	guard, err := platform.NewPathGuard(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := guard.Root()
	payload, err := TaskCommandPayload("patch journal test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginCommandRun(context.Background(), CommandRunMetadata{
		SessionID: "session-patch", CommandID: "command-patch", CommandKind: CommandKindExec,
		CommandPayload: payload, RunID: "run-patch", ProjectRoot: root,
		Provider: "openai-compatible", Model: "test-model", StartedAt: storeTestTime,
	}); err != nil {
		t.Fatal(err)
	}
	recorder, err := store.NewPatchJournalRecorder(context.Background(), "session-patch", "run-patch", "command-patch", root)
	if err != nil {
		t.Fatal(err)
	}
	return store, recorder, root
}

func patchTestIntent(t *testing.T, path string, preExists bool, before, after string) tools.MutationIntent {
	t.Helper()
	intent := tools.MutationIntent{
		ToolCallID: "call-patch", ToolName: "write_file", CallDigest: strings.Repeat("a", 64),
		Path: path, PreExists: preExists, PostExists: true,
		PostSHA256: bytesDigest(after), PostMode: 0o600,
	}
	if preExists {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		intent.PreSHA256, intent.PreMode = bytesDigest(before), info.Mode().Perm()
		intent.PostMode = info.Mode().Perm()
		intent.PreContent = []byte(before)
	}
	return intent
}

func bytesDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
