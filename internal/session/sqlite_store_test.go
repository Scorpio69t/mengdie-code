// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var storeTestTime = time.Date(2026, 8, 6, 10, 30, 0, 123, time.UTC)

func TestOpenSQLiteAppliesSchemaAndConnectionSettings(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 250*time.Millisecond)
	defer closeTestStore(t, store)
	for _, table := range []string{"schema_migrations", "sessions", "runs", "events", "commands", "snapshots", "context_messages", "artifacts", "context_summaries", "patch_journals", "patch_entries"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %s count=%d", table, count)
		}
	}
	var migrationCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 8 {
		t.Fatalf("migration count=%d", migrationCount)
	}
	if _, err := os.Stat(store.Path()); err != nil {
		t.Fatalf("database path: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(store.Path())
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("database mode=%o, want 600", info.Mode().Perm())
		}
	}
}

func TestSQLiteStoreAppendLoadAndTerminalIndexes(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginTestRun(t, store)
	records := []Record{
		testRecord(1, 1, "evt-1", "run.started"),
		testRecord(2, 3, "evt-2", "message.completed"),
		testRecord(3, 4, "evt-3", "run.completed"),
	}
	if err := store.Append(context.Background(), "session-1", 0, records); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), "session-1", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].ID != "evt-2" || loaded[0].SessionSeq != 2 || loaded[0].RunSeq != 3 {
		t.Fatalf("loaded=%+v", loaded)
	}
	loaded[0].Payload[0] = 'x'
	again, err := store.Load(context.Background(), "session-1", 1, 1)
	if err != nil || string(again[0].Payload) != `{"value":true}` {
		t.Fatalf("defensive load=%q err=%v", again[0].Payload, err)
	}
	var sessionStatus, runStatus string
	var lastSeq, lastRunSeq uint64
	if err := store.db.QueryRow(`SELECT status, last_seq FROM sessions WHERE id='session-1'`).Scan(&sessionStatus, &lastSeq); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT status, last_run_seq FROM runs WHERE id='run-1'`).Scan(&runStatus, &lastRunSeq); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "completed" || runStatus != "completed" || lastSeq != 3 || lastRunSeq != 4 {
		t.Fatalf("session=(%s,%d) run=(%s,%d)", sessionStatus, lastSeq, runStatus, lastRunSeq)
	}
}

func TestSQLiteStoreSequenceConflictAndDuplicateRollback(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginTestRun(t, store)
	if err := store.Append(context.Background(), "session-1", 0, []Record{testRecord(1, 1, "evt-1", "run.started")}); err != nil {
		t.Fatal(err)
	}
	conflictRecord := testRecord(1, 2, "evt-conflict", "warning")
	err := store.Append(context.Background(), "session-1", 0, []Record{conflictRecord})
	var conflict *SequenceConflictError
	if !errors.As(err, &conflict) || conflict.Expected != 0 || conflict.Actual != 1 {
		t.Fatalf("Append(conflict)=%v", err)
	}
	duplicate := testRecord(2, 2, "evt-1", "warning")
	err = store.Append(context.Background(), "session-1", 1, []Record{duplicate})
	if !errors.Is(err, ErrDuplicateEvent) {
		t.Fatalf("Append(duplicate)=%v", err)
	}
	if err := store.Append(context.Background(), "session-1", 1, []Record{testRecord(2, 2, "evt-2", "warning")}); err != nil {
		t.Fatalf("append after rollback: %v", err)
	}
}

func TestSQLiteStoreBusyIsBoundedAndClassified(t *testing.T) {
	directory := t.TempDir()
	first := openTestStore(t, directory, 20*time.Millisecond)
	defer closeTestStore(t, first)
	second := openTestStore(t, directory, 20*time.Millisecond)
	defer closeTestStore(t, second)
	beginTestRun(t, first)
	connection, err := first.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, rollbackErr := connection.ExecContext(context.Background(), `ROLLBACK`); rollbackErr != nil {
			t.Errorf("rollback lock transaction: %v", rollbackErr)
		}
		if closeErr := connection.Close(); closeErr != nil {
			t.Errorf("close lock connection: %v", closeErr)
		}
	}()
	started := time.Now()
	err = second.Append(context.Background(), "session-1", 0, []Record{testRecord(1, 1, "evt-1", "run.started")})
	if !errors.Is(err, ErrStoreBusy) {
		t.Fatalf("Append(busy)=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("busy wait=%s, want bounded", elapsed)
	}
}

func TestSQLiteStoreRejectsCorruptPersistedPayload(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginTestRun(t, store)
	if err := store.Append(context.Background(), "session-1", 0, []Record{testRecord(1, 1, "evt-1", "run.started")}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE events SET payload_json='{' WHERE event_id='evt-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), "session-1", 0, 10); err == nil || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("Load(corrupt)=%v", err)
	}
}

func TestBeginRunIsIdempotentOnlyForSameIdentity(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	metadata := testRunMetadata(t)
	if err := store.BeginRun(context.Background(), metadata); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRun(context.Background(), metadata); err != nil {
		t.Fatalf("same BeginRun=%v", err)
	}
	metadata.Model = "different"
	if err := store.BeginRun(context.Background(), metadata); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("different BeginRun=%v", err)
	}
}

func TestMigrationChecksumAndFutureVersionFailClosed(t *testing.T) {
	t.Run("checksum drift", func(t *testing.T) {
		store := openTestStore(t, t.TempDir(), 0)
		defer closeTestStore(t, store)
		if _, err := store.db.Exec(`UPDATE schema_migrations SET sha256='changed' WHERE version=1`); err != nil {
			t.Fatal(err)
		}
		if err := applyMigrations(context.Background(), store.db, embeddedMigrations, time.Now); !errors.Is(err, ErrMigrationDrift) {
			t.Fatalf("applyMigrations(drift)=%v", err)
		}
	})
	t.Run("future version", func(t *testing.T) {
		store := openTestStore(t, t.TempDir(), 0)
		defer closeTestStore(t, store)
		if _, err := store.db.Exec(`INSERT INTO schema_migrations(version,name,sha256,applied_at) VALUES(99,'future.sql','x','2026-08-06T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if err := applyMigrations(context.Background(), store.db, embeddedMigrations, time.Now); !errors.Is(err, ErrSchemaTooNew) {
			t.Fatalf("applyMigrations(future)=%v", err)
		}
	})
}

func TestArtifactMigrationUpgradesExistingContextLedger(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, databaseFilename)
	db, err := sql.Open("sqlite", sqliteDSN(databasePath, defaultBusyTimeout))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := fstest.MapFS{}
	for _, name := range []string{
		"001_session_event_store.sql",
		"002_command_ledger_snapshot.sql",
		"003_context_messages.sql",
	} {
		content, err := embeddedMigrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		legacy["migrations/"+name] = &fstest.MapFile{Data: content}
	}
	if err := applyMigrations(context.Background(), db, legacy, func() time.Time { return storeTestTime }); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenSQLite(context.Background(), OpenOptions{
		DataDir: directory, ProjectRoot: filepath.Join(t.TempDir(), "project"),
		BusyTimeout: 250 * time.Millisecond, Now: func() time.Time { return storeTestTime },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestStore(t, store)
	var migrationCount, artifactColumnCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('context_messages') WHERE name='artifact_id'`).Scan(&artifactColumnCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 8 || artifactColumnCount != 1 {
		t.Fatalf("migration count=%d artifact columns=%d", migrationCount, artifactColumnCount)
	}
}

func TestFailedMigrationRollsBackItsSchemaChanges(t *testing.T) {
	db, err := sql.Open("sqlite", "file:failed-migration?mode=memory&cache=shared&_foreign_keys=on&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close migration database: %v", closeErr)
		}
	}()
	source := fstest.MapFS{
		"migrations/001_broken.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE should_rollback(id INTEGER); INVALID SQL;`)},
	}
	err = applyMigrations(context.Background(), db, source, time.Now)
	if err == nil {
		t.Fatal("broken migration succeeded")
	}
	var count int
	if queryErr := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='should_rollback'`).Scan(&count); queryErr != nil {
		t.Fatal(queryErr)
	}
	if count != 0 {
		t.Fatalf("failed migration left table count=%d", count)
	}
}

func TestReadMigrationsRequiresConsecutiveVersions(t *testing.T) {
	source := fstest.MapFS{"migrations/002_gap.sql": &fstest.MapFile{Data: []byte(`SELECT 1;`)}}
	if _, err := readMigrations(source); err == nil {
		t.Fatal("readMigrations(gap) succeeded")
	}
	_, err := readMigrations(fstest.MapFS{})
	if err == nil {
		t.Fatal("readMigrations(missing) succeeded")
	}
}

func TestMigrationNameDriftFailsClosed(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	if _, err := store.db.Exec(`UPDATE schema_migrations SET name='001_renamed.sql' WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(context.Background(), store.db, embeddedMigrations, time.Now); !errors.Is(err, ErrMigrationDrift) {
		t.Fatalf("applyMigrations(name drift)=%v", err)
	}
}

func TestSQLiteStoreRejectsEventsAfterTerminalRun(t *testing.T) {
	store := openTestStore(t, t.TempDir(), 0)
	defer closeTestStore(t, store)
	beginTestRun(t, store)
	if err := store.Append(context.Background(), "session-1", 0, []Record{testRecord(1, 1, "evt-1", "run.completed")}); err != nil {
		t.Fatal(err)
	}
	err := store.Append(context.Background(), "session-1", 1, []Record{testRecord(2, 2, "evt-2", "warning")})
	if !errors.Is(err, ErrRunConflict) {
		t.Fatalf("Append(after terminal)=%v", err)
	}
	loaded, loadErr := store.Load(context.Background(), "session-1", 0, 10)
	if loadErr != nil || len(loaded) != 1 {
		t.Fatalf("Load() records=%d err=%v", len(loaded), loadErr)
	}
}

func TestSQLiteErrorClassification(t *testing.T) {
	if !errors.Is(classifySQLiteError("append", codedTestError{code: 5}), ErrStoreBusy) {
		t.Fatal("SQLITE_BUSY not classified")
	}
	if !errors.Is(classifySQLiteError("append", codedTestError{code: 13}), ErrStoreFull) {
		t.Fatal("SQLITE_FULL not classified")
	}
}

func openTestStore(t *testing.T, directory string, timeout time.Duration) *SQLiteStore {
	t.Helper()
	store, err := OpenSQLite(context.Background(), OpenOptions{
		DataDir: directory, BusyTimeout: timeout, Now: func() time.Time { return storeTestTime },
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func closeTestStore(t *testing.T, store *SQLiteStore) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Errorf("close SQLite store: %v", err)
	}
}

func beginTestRun(t *testing.T, store *SQLiteStore) {
	t.Helper()
	if err := store.BeginRun(context.Background(), testRunMetadata(t)); err != nil {
		t.Fatal(err)
	}
}

func testRunMetadata(t *testing.T) RunMetadata {
	t.Helper()
	return RunMetadata{
		SessionID: "session-1", RunID: "run-1", ProjectRoot: filepath.Clean(t.TempDir()),
		Provider: "openai-compatible", Model: "test-model", StartedAt: storeTestTime,
	}
}

func testRecord(sessionSeq, runSeq uint64, id, kind string) Record {
	return Record{
		ID: id, SessionID: "session-1", SessionSeq: sessionSeq, RunID: "run-1", RunSeq: runSeq,
		Kind: kind, SchemaVersion: 1, Visibility: VisibilityPublic,
		Payload: json.RawMessage(`{"value":true}`), Time: storeTestTime.Add(time.Duration(sessionSeq) * time.Second),
	}
}

type codedTestError struct{ code int }

func (errorValue codedTestError) Error() string { return "sqlite test error" }
func (errorValue codedTestError) Code() int     { return errorValue.code }
