// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

func TestContextRecorderOffloadsLargeMessageAndRoundTrips(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginContextCommand(t, store)
	recorder, err := store.NewContextRecorder(context.Background(), "session-context", "run-context", "command-context")
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("梦蝶上下文", 16_000)
	message := provider.Message{Role: provider.RoleTool, ToolCallID: "call-large", Name: "shell", Content: content}
	if err := recorder.RecordMessage(context.Background(), message, true); err != nil {
		t.Fatal(err)
	}

	var artifactID, relative, sensitivity string
	var inlineBytes, artifactBytes int64
	if err := store.db.QueryRow(`
SELECT cm.artifact_id, length(cm.message_json), a.relative_path, a.size_bytes, a.sensitivity
FROM context_messages cm JOIN artifacts a ON a.id=cm.artifact_id
WHERE cm.session_id='session-context'`).Scan(
		&artifactID, &inlineBytes, &relative, &artifactBytes, &sensitivity,
	); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(artifactID, "art_") || inlineBytes >= inlineContextMessageBytes || artifactBytes <= inlineContextMessageBytes {
		t.Fatalf("artifact=%q inline=%d artifact_bytes=%d", artifactID, inlineBytes, artifactBytes)
	}
	if sensitivity != string(VisibilityPrivate) {
		t.Fatalf("artifact sensitivity=%q", sensitivity)
	}
	full, err := store.resolveArtifactPath(relative)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact mode=%o, want 600", info.Mode().Perm())
	}
	if runtime.GOOS != "windows" {
		directoryInfo, err := os.Stat(store.artifactDir)
		if err != nil {
			t.Fatal(err)
		}
		if directoryInfo.Mode().Perm() != 0o700 {
			t.Fatalf("artifact directory mode=%o, want 700", directoryInfo.Mode().Perm())
		}
	}

	loaded, err := store.LoadContext(context.Background(), "session-context")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Message.Content != content || loaded[0].Message.ToolCallID != "call-large" {
		t.Fatalf("large context round trip mismatch: %+v", loaded)
	}
}

func TestContextArtifactIntegrityFailuresFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "missing", mutate: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "tampered", mutate: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t, t.TempDir(), 0)
			defer closeTestStore(t, store)
			beginContextCommand(t, store)
			recorder, err := store.NewContextRecorder(context.Background(), "session-context", "run-context", "command-context")
			if err != nil {
				t.Fatal(err)
			}
			if err := recorder.RecordMessage(context.Background(), largeContextMessage(), true); err != nil {
				t.Fatal(err)
			}
			path := contextArtifactPath(t, store)
			test.mutate(t, path)
			if _, err := store.LoadContext(context.Background(), "session-context"); !errors.Is(err, ErrContextCorrupt) {
				t.Fatalf("LoadContext() error=%v", err)
			}
		})
	}
}

func TestContextArtifactQuotaAndConflictLeaveNoFiles(t *testing.T) {
	t.Run("quota", func(t *testing.T) {
		directory := t.TempDir()
		store, err := OpenSQLite(context.Background(), OpenOptions{
			DataDir: directory, ProjectRoot: filepath.Join(t.TempDir(), "project"),
			SessionArtifactQuotaBytes: inlineContextMessageBytes,
			GlobalArtifactQuotaBytes:  inlineContextMessageBytes,
			Now:                       func() time.Time { return storeTestTime },
		})
		if err != nil {
			t.Fatal(err)
		}
		defer closeTestStore(t, store)
		beginContextCommand(t, store)
		recorder, err := store.NewContextRecorder(context.Background(), "session-context", "run-context", "command-context")
		if err != nil {
			t.Fatal(err)
		}
		if err := recorder.RecordMessage(context.Background(), largeContextMessage(), true); !errors.Is(err, ErrArtifactQuota) {
			t.Fatalf("RecordMessage(quota)=%v", err)
		}
		assertNoManagedArtifactFiles(t, store)
	})

	t.Run("optimistic conflict", func(t *testing.T) {
		store := openTestStore(t, t.TempDir(), 0)
		defer closeTestStore(t, store)
		beginContextCommand(t, store)
		current, err := store.NewContextRecorder(context.Background(), "session-context", "run-context", "command-context")
		if err != nil {
			t.Fatal(err)
		}
		stale, err := store.NewContextRecorder(context.Background(), "session-context", "run-context", "command-context")
		if err != nil {
			t.Fatal(err)
		}
		if err := current.RecordMessage(context.Background(), provider.Message{Role: provider.RoleUser, Content: "current"}, true); err != nil {
			t.Fatal(err)
		}
		if err := stale.RecordMessage(context.Background(), largeContextMessage(), true); !errors.Is(err, ErrContextConflict) {
			t.Fatalf("RecordMessage(conflict)=%v", err)
		}
		assertNoManagedArtifactFiles(t, store)
	})
}

func TestArtifactStartupReconciliationRemovesOnlyManagedOrphans(t *testing.T) {
	directory := t.TempDir()
	store := openTestStore(t, directory, 0)
	orphan := filepath.Join(store.artifactDir, "art_"+strings.Repeat("a", 64)+".json")
	fresh := filepath.Join(store.artifactDir, "art_"+strings.Repeat("b", 64)+".json")
	temporary := filepath.Join(store.artifactDir, ".tmp-artifact-abandoned")
	unknown := filepath.Join(store.artifactDir, "README.txt")
	for _, path := range []string{orphan, fresh, temporary, unknown} {
		if err := os.WriteFile(path, []byte("orphan"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := storeTestTime.Add(-2 * artifactOrphanGracePeriod)
	for _, path := range []string{orphan, temporary} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	closeTestStore(t, store)

	store = openTestStore(t, directory, 0)
	defer closeTestStore(t, store)
	for _, path := range []string{orphan, temporary} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("managed orphan %s remains: %v", path, err)
		}
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("unknown file was removed: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh managed file was removed inside grace period: %v", err)
	}
}

func TestSessionDeleteRemovesArtifactsAndReportsResidue(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		store := openTestStore(t, t.TempDir(), 0)
		defer closeTestStore(t, store)
		beginContextCommand(t, store)
		recordLargeContext(t, store)
		artifactPath := contextArtifactPath(t, store)
		service, err := NewService(store)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Delete(context.Background(), "session-context"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(artifactPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("artifact remains after delete: %v", err)
		}
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifacts`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("artifact rows=%d err=%v", count, err)
		}
	})

	t.Run("residue", func(t *testing.T) {
		store := openTestStore(t, t.TempDir(), 0)
		defer closeTestStore(t, store)
		beginContextCommand(t, store)
		recordLargeContext(t, store)
		artifactPath := contextArtifactPath(t, store)
		if err := os.Remove(artifactPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(artifactPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(artifactPath, "residue"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		service, err := NewService(store)
		if err != nil {
			t.Fatal(err)
		}
		err = service.Delete(context.Background(), "session-context")
		var cleanup *ArtifactCleanupError
		if !errors.As(err, &cleanup) || len(cleanup.Paths) != 1 {
			t.Fatalf("Delete(residue)=%v", err)
		}
		if _, err := service.View(context.Background(), "session-context"); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("session was not durably deleted: %v", err)
		}
	})
}

func TestArtifactRelativePathCannotEscapeDataRoot(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginContextCommand(t, store)
	recordLargeContext(t, store)
	if _, err := store.db.Exec(`UPDATE artifacts SET relative_path='../escape.json'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadContext(context.Background(), "session-context"); !errors.Is(err, ErrContextCorrupt) {
		t.Fatalf("LoadContext(path escape)=%v", err)
	}
}

func largeContextMessage() provider.Message {
	return provider.Message{Role: provider.RoleUser, Content: strings.Repeat("large-context-", 6_000)}
}

func recordLargeContext(t *testing.T, store *SQLiteStore) {
	t.Helper()
	recorder, err := store.NewContextRecorder(context.Background(), "session-context", "run-context", "command-context")
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessage(context.Background(), largeContextMessage(), true); err != nil {
		t.Fatal(err)
	}
}

func contextArtifactPath(t *testing.T, store *SQLiteStore) string {
	t.Helper()
	var relative string
	if err := store.db.QueryRow(`SELECT relative_path FROM artifacts LIMIT 1`).Scan(&relative); err != nil {
		t.Fatal(err)
	}
	full, err := store.resolveArtifactPath(relative)
	if err != nil {
		t.Fatal(err)
	}
	return full
}

func assertNoManagedArtifactFiles(t *testing.T, store *SQLiteStore) {
	t.Helper()
	entries, err := os.ReadDir(store.artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "art_") || strings.HasPrefix(entry.Name(), ".tmp-artifact-") {
			t.Fatalf("managed artifact remains after failed transaction: %s", entry.Name())
		}
	}
}
