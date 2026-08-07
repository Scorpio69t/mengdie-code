// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrMigrationDrift means an already-applied migration no longer matches
	// the bytes embedded in the running binary.
	ErrMigrationDrift = errors.New("session migration checksum drift")
	// ErrSchemaTooNew prevents an older binary from silently opening data it
	// cannot interpret.
	ErrSchemaTooNew = errors.New("session database schema is newer than this binary")
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

const migrationTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY NOT NULL,
    name       TEXT NOT NULL UNIQUE,
    sha256     TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`

type migration struct {
	version  int
	name     string
	checksum string
	content  []byte
}

type appliedMigration struct {
	name     string
	checksum string
}

func applyMigrations(ctx context.Context, db *sql.DB, source fs.FS, now func() time.Time) error {
	if _, err := db.ExecContext(ctx, migrationTableSQL); err != nil {
		return classifySQLiteError("create migration ledger", err)
	}
	migrations, err := readMigrations(source)
	if err != nil {
		return err
	}
	applied, err := loadAppliedMigrations(ctx, db)
	if err != nil {
		return err
	}
	latest := 0
	if len(migrations) > 0 {
		latest = migrations[len(migrations)-1].version
	}
	for version := range applied {
		if version > latest {
			return fmt.Errorf("%w: database=%d binary=%d", ErrSchemaTooNew, version, latest)
		}
	}
	for _, item := range migrations {
		if existing, ok := applied[item.version]; ok {
			if existing.name != item.name || existing.checksum != item.checksum {
				return fmt.Errorf("%w: version=%d name=%s", ErrMigrationDrift, item.version, item.name)
			}
			continue
		}
		if err := applyMigration(ctx, db, item, now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func readMigrations(source fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(source, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read session migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		separator := strings.IndexByte(entry.Name(), '_')
		if separator <= 0 {
			return nil, fmt.Errorf("invalid session migration name %q", entry.Name())
		}
		version, err := strconv.Atoi(entry.Name()[:separator])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid session migration version in %q", entry.Name())
		}
		content, err := fs.ReadFile(source, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read session migration %q: %w", entry.Name(), err)
		}
		digest := sha256.Sum256(content)
		result = append(result, migration{
			version: version, name: entry.Name(), checksum: fmt.Sprintf("%x", digest[:]), content: content,
		})
	}
	for index, item := range result {
		if item.version != index+1 {
			return nil, fmt.Errorf("session migration versions must be consecutive from 1: got %d at index %d", item.version, index)
		}
	}
	return result, nil
}

func loadAppliedMigrations(ctx context.Context, db *sql.DB) (result map[int]appliedMigration, resultErr error) {
	rows, err := db.QueryContext(ctx, `SELECT version, name, sha256 FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, classifySQLiteError("load migration ledger", err)
	}
	defer closeRows(rows, &resultErr, "close migration ledger rows")
	result = make(map[int]appliedMigration)
	for rows.Next() {
		var version int
		var item appliedMigration
		if err := rows.Scan(&version, &item.name, &item.checksum); err != nil {
			return nil, fmt.Errorf("scan migration ledger: %w", err)
		}
		result[version] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration ledger: %w", err)
	}
	return result, nil
}

func applyMigration(ctx context.Context, db *sql.DB, item migration, appliedAt time.Time) (resultErr error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteError("begin migration", err)
	}
	defer rollbackTransaction(tx, &resultErr, "rollback session migration")
	if _, err := tx.ExecContext(ctx, string(item.content)); err != nil {
		return classifySQLiteError(fmt.Sprintf("apply migration %s", item.name), err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, sha256, applied_at) VALUES (?, ?, ?, ?)`,
		item.version, item.name, item.checksum, formatTime(appliedAt),
	); err != nil {
		return classifySQLiteError(fmt.Sprintf("record migration %s", item.name), err)
	}
	if err := tx.Commit(); err != nil {
		return classifySQLiteError(fmt.Sprintf("commit migration %s", item.name), err)
	}
	return nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
