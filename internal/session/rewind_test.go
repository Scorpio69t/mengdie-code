// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func TestRewindLifecycleRestoresVerifiedPatch(t *testing.T) {
	store, recorder, root := newPatchJournalHarness(t)
	defer closeTestStore(t, store)
	path := filepath.Join(root, "value.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt := verifyPatchForRewind(t, recorder, path, true, "before\n", "after\n")
	journalID, err := store.ResolveRewindJournal(context.Background(), "session-patch", root)
	if err != nil || journalID != receipt.JournalID {
		t.Fatalf("ResolveRewindJournal()=%q, %v", journalID, err)
	}
	target, err := store.InspectRewind(context.Background(), "session-patch", journalID, root)
	if err != nil || string(target.PreContent) != "before\n" || string(target.PostContent) != "after\n" {
		t.Fatalf("InspectRewind()=%+v, %v", target, err)
	}
	begin, err := store.BeginRewindCommand(context.Background(), "session-patch", journalID, "rewind-1", root)
	if err != nil || begin.Existing {
		t.Fatalf("BeginRewindCommand()=%+v, %v", begin, err)
	}
	duplicate, err := store.BeginRewindCommand(context.Background(), "session-patch", journalID, "rewind-1", root)
	if err != nil || !duplicate.Existing {
		t.Fatalf("duplicate BeginRewindCommand()=%+v, %v", duplicate, err)
	}
	if err := store.StartRewind(context.Background(), "session-patch", journalID, "rewind-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, target.PreContent, target.PreMode); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRewind(context.Background(), "session-patch", journalID, "rewind-1"); err != nil {
		t.Fatal(err)
	}
	command, err := store.LookupCommand(context.Background(), "rewind-1")
	if err != nil || command.Status != CommandApplied {
		t.Fatalf("command=%+v err=%v", command, err)
	}
	entry, err := store.loadPatchJournal(context.Background(), journalID)
	if err != nil || entry.Status != PatchRewound {
		t.Fatalf("entry=%+v err=%v", entry, err)
	}
	if _, err := store.InspectRewind(context.Background(), "session-patch", journalID, root); !errors.Is(err, ErrRewindUnavailable) {
		t.Fatalf("InspectRewind(after rewind) error=%v", err)
	}
}

func TestRewindRecoveryNeverReplaysMutation(t *testing.T) {
	tests := []struct {
		name          string
		current       string
		wantCommand   CommandStatus
		wantPatch     PatchStatus
		wantAvailable bool
	}{
		{name: "pre state means completed", current: "before\n", wantCommand: CommandApplied, wantPatch: PatchRewound},
		{name: "post state means interrupted before mutation", current: "after\n", wantCommand: CommandInterrupted, wantPatch: PatchVerified, wantAvailable: true},
		{name: "other state is conflict", current: "user edit\n", wantCommand: CommandFailed, wantPatch: PatchConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, recorder, root := newPatchJournalHarness(t)
			defer closeTestStore(t, store)
			path := filepath.Join(root, "value.txt")
			if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			receipt := verifyPatchForRewind(t, recorder, path, true, "before\n", "after\n")
			if _, err := store.BeginRewindCommand(context.Background(), "session-patch", receipt.JournalID, "rewind-recover", root); err != nil {
				t.Fatal(err)
			}
			if err := store.StartRewind(context.Background(), "session-patch", receipt.JournalID, "rewind-recover"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.current), 0o600); err != nil {
				t.Fatal(err)
			}
			command, err := store.RecoverRewindCommand(context.Background(), "rewind-recover", root)
			if err != nil || command.Status != test.wantCommand {
				t.Fatalf("RecoverRewindCommand()=%+v, %v", command, err)
			}
			entry, err := store.loadPatchJournal(context.Background(), receipt.JournalID)
			if err != nil || entry.Status != test.wantPatch {
				t.Fatalf("entry=%+v err=%v", entry, err)
			}
			_, inspectErr := store.InspectRewind(context.Background(), "session-patch", receipt.JournalID, root)
			if test.wantAvailable != (inspectErr == nil) {
				t.Fatalf("InspectRewind() error=%v wantAvailable=%t", inspectErr, test.wantAvailable)
			}
		})
	}
}

func TestPatchJournalStoresLargeRewindMaterialAsPrivateArtifact(t *testing.T) {
	store, recorder, root := newPatchJournalHarness(t)
	defer closeTestStore(t, store)
	path := filepath.Join(root, "large.txt")
	before := strings.Repeat("a", inlinePatchRewindBytes+1)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt := verifyPatchForRewind(t, recorder, path, true, before, "after\n")
	entry, err := store.loadPatchJournal(context.Background(), receipt.JournalID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ReverseArtifact == "" || entry.ReverseInlineSet {
		t.Fatalf("rewind material artifact=%q inline=%t", entry.ReverseArtifact, entry.ReverseInlineSet)
	}
	target, err := store.InspectRewind(context.Background(), "session-patch", receipt.JournalID, root)
	if err != nil || string(target.PreContent) != before {
		t.Fatalf("InspectRewind() bytes=%d err=%v", len(target.PreContent), err)
	}
	if _, err := recorder.Prepare(context.Background(), patchTestIntent(t, path, true, before, "after\n")); err == nil {
		t.Fatal("duplicate large Journal unexpectedly replaced its Artifact")
	}
	if _, err := store.InspectRewind(context.Background(), "session-patch", receipt.JournalID, root); err != nil {
		t.Fatalf("duplicate Prepare damaged committed rewind material: %v", err)
	}
	var relativePath string
	if err := store.db.QueryRow(`SELECT relative_path FROM artifacts WHERE id=?`, entry.ReverseArtifact).Scan(&relativePath); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(store.dataDir, filepath.FromSlash(relativePath))
	if err := os.WriteFile(artifactPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InspectRewind(context.Background(), "session-patch", receipt.JournalID, root); !errors.Is(err, ErrArtifactCorrupt) {
		t.Fatalf("InspectRewind(tampered artifact) error=%v", err)
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), "session-patch"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifactPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rewind artifact survived session deletion: %v", err)
	}
}

func verifyPatchForRewind(t *testing.T, recorder *PatchJournalRecorder, path string, existed bool, before, after string) tools.MutationReceipt {
	t.Helper()
	receipt, err := recorder.Prepare(context.Background(), patchTestIntent(t, path, existed, before, after))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(after), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recorder.MarkApplied(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	if err := recorder.VerifyPost(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}
