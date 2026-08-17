// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	artifactDirectoryName     = "artifacts"
	maxArtifactBytes          = 16 << 20
	artifactOrphanGracePeriod = time.Hour
)

var (
	ErrArtifactNotFound = errors.New("session artifact not found")
	ErrArtifactCorrupt  = errors.New("session artifact is corrupt")
	ErrArtifactQuota    = errors.New("session artifact quota exceeded")
)

type artifactMetadata struct {
	ID           string
	SessionID    string
	RunID        string
	Kind         string
	MIME         string
	Sensitivity  Visibility
	RelativePath string
	SHA256       string
	SizeBytes    int64
}

// ArtifactCleanupReport records only internally generated relative paths. It
// never exposes artifact contents.
type ArtifactCleanupReport struct {
	Removed []string
}

// ArtifactCleanupError means durable Session deletion succeeded but one or
// more already-unreferenced files remain for manual cleanup.
type ArtifactCleanupError struct {
	Paths []string
	Err   error
}

func (e *ArtifactCleanupError) Error() string {
	return fmt.Sprintf("session deleted but artifact cleanup failed for %s: %v", strings.Join(e.Paths, ", "), e.Err)
}

func (e *ArtifactCleanupError) Unwrap() error { return e.Err }

func (s *SQLiteStore) prepareArtifactDirectory() error {
	if err := prepareDataDir(s.artifactDir); err != nil {
		return fmt.Errorf("prepare artifact directory: %w", err)
	}
	return nil
}

func artifactChecksum(content []byte) string {
	digest := sha256.Sum256(content)
	return fmt.Sprintf("sha256:%x", digest[:])
}

func contextArtifactID(contextID, checksum string) string {
	digest := sha256.Sum256([]byte(contextID + "\x00" + checksum))
	return fmt.Sprintf("art_%x", digest[:])
}

func artifactRelativePath(id string) (string, error) {
	if !strings.HasPrefix(id, "art_") || len(id) != 68 {
		return "", errors.New("artifact id is invalid")
	}
	for _, char := range id[4:] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", errors.New("artifact id is invalid")
		}
	}
	return filepath.ToSlash(filepath.Join(artifactDirectoryName, id+".json")), nil
}

func (s *SQLiteStore) resolveArtifactPath(relative string) (string, error) {
	if err := validateDataDir(s.artifactDir, ""); err != nil {
		return "", fmt.Errorf("%w: unsafe artifact root: %v", ErrArtifactCorrupt, err)
	}
	if relative == "" || filepath.IsAbs(relative) || strings.Contains(relative, `\`) {
		return "", fmt.Errorf("%w: invalid relative path", ErrArtifactCorrupt)
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	wantPrefix := artifactDirectoryName + string(filepath.Separator)
	if clean == artifactDirectoryName || !strings.HasPrefix(clean, wantPrefix) {
		return "", fmt.Errorf("%w: path escapes artifact root", ErrArtifactCorrupt)
	}
	full := filepath.Join(s.dataDir, clean)
	rel, err := filepath.Rel(s.artifactDir, full)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path escapes artifact root", ErrArtifactCorrupt)
	}
	return full, nil
}

func (s *SQLiteStore) stageArtifactFile(id string, content []byte) (relative string, cleanup func(), err error) {
	if len(content) == 0 {
		return "", nil, errors.New("artifact content is empty")
	}
	if len(content) > maxArtifactBytes {
		return "", nil, fmt.Errorf("artifact exceeds %d bytes", maxArtifactBytes)
	}
	if err := s.prepareArtifactDirectory(); err != nil {
		return "", nil, err
	}
	relative, err = artifactRelativePath(id)
	if err != nil {
		return "", nil, err
	}
	target, err := s.resolveArtifactPath(relative)
	if err != nil {
		return "", nil, err
	}
	if _, err := os.Lstat(target); err == nil {
		return "", nil, errors.New("artifact file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("inspect artifact target: %w", err)
	}
	temporary, err := os.CreateTemp(s.artifactDir, ".tmp-artifact-")
	if err != nil {
		return "", nil, fmt.Errorf("create artifact temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	closed := false
	defer func() {
		if !closed {
			if closeErr := temporary.Close(); err == nil && closeErr != nil {
				err = fmt.Errorf("close artifact temporary file: %w", closeErr)
			}
		}
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if runtime.GOOS != "windows" {
		if err := temporary.Chmod(0o600); err != nil {
			return "", nil, fmt.Errorf("protect artifact temporary file: %w", err)
		}
	}
	if _, err := temporary.Write(content); err != nil {
		return "", nil, fmt.Errorf("write artifact: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return "", nil, fmt.Errorf("sync artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", nil, fmt.Errorf("close artifact: %w", err)
	}
	closed = true
	// A hard link gives us an atomic no-clobber commit on every supported
	// platform. Rename would replace an existing target on Unix if two
	// writers raced after the Lstat check.
	if err := os.Link(temporaryPath, target); err != nil {
		return "", nil, fmt.Errorf("commit artifact file: %w", err)
	}
	committed = true
	if err := os.Remove(temporaryPath); err != nil {
		_ = os.Remove(target)
		committed = false
		return "", nil, fmt.Errorf("remove artifact staging link: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, 0o600); err != nil {
			_ = os.Remove(target)
			return "", nil, fmt.Errorf("protect artifact file: %w", err)
		}
	}
	if err := syncArtifactDirectory(s.artifactDir); err != nil {
		_ = os.Remove(target)
		return "", nil, err
	}
	return relative, func() { _ = os.Remove(target) }, nil
}

func syncArtifactDirectory(directory string) (resultErr error) {
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open artifact directory for sync: %w", err)
	}
	defer func() {
		if err := handle.Close(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("close artifact directory: %w", err)
		}
	}()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync artifact directory: %w", err)
	}
	return nil
}

func (s *SQLiteStore) insertArtifactTx(ctx context.Context, tx *sql.Tx, item artifactMetadata) error {
	if item.SizeBytes <= 0 || item.SizeBytes > maxArtifactBytes {
		return errors.New("artifact size is invalid")
	}
	var globalUsed, sessionUsed int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes),0) FROM artifacts WHERE deleted_at IS NULL`).Scan(&globalUsed); err != nil {
		return classifySQLiteError("load global artifact quota", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes),0) FROM artifacts WHERE session_id=? AND deleted_at IS NULL`, item.SessionID).Scan(&sessionUsed); err != nil {
		return classifySQLiteError("load session artifact quota", err)
	}
	if item.SizeBytes > s.globalArtifactQuotaBytes-globalUsed || item.SizeBytes > s.sessionArtifactQuotaBytes-sessionUsed {
		return fmt.Errorf("%w: size=%d session_used=%d session_limit=%d global_used=%d global_limit=%d",
			ErrArtifactQuota, item.SizeBytes, sessionUsed, s.sessionArtifactQuotaBytes, globalUsed, s.globalArtifactQuotaBytes)
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO artifacts(
    id, session_id, run_id, kind, mime, sensitivity, relative_path,
    sha256, size_bytes, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.SessionID, item.RunID, item.Kind, item.MIME, string(item.Sensitivity),
		item.RelativePath, item.SHA256, item.SizeBytes, formatTime(s.now().UTC()),
	)
	if err != nil {
		return classifySQLiteError("insert artifact", err)
	}
	return nil
}

func (s *SQLiteStore) readArtifactFile(item artifactMetadata) ([]byte, error) {
	if item.SizeBytes <= 0 || item.SizeBytes > maxArtifactBytes {
		return nil, fmt.Errorf("%w: invalid size", ErrArtifactCorrupt)
	}
	full, err := s.resolveArtifactPath(item.RelativePath)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrArtifactNotFound, item.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != item.SizeBytes {
		return nil, fmt.Errorf("%w: metadata mismatch", ErrArtifactCorrupt)
	}
	content, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	if artifactChecksum(content) != item.SHA256 {
		return nil, fmt.Errorf("%w: checksum mismatch", ErrArtifactCorrupt)
	}
	return content, nil
}

func (s *SQLiteStore) reconcileArtifacts(ctx context.Context) (ArtifactCleanupReport, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT relative_path FROM artifacts WHERE deleted_at IS NULL`)
	if err != nil {
		return ArtifactCleanupReport{}, classifySQLiteError("list registered artifacts", err)
	}
	expected := make(map[string]struct{})
	for rows.Next() {
		var relative string
		if err := rows.Scan(&relative); err != nil {
			_ = rows.Close()
			return ArtifactCleanupReport{}, fmt.Errorf("scan registered artifact: %w", err)
		}
		if _, err := s.resolveArtifactPath(relative); err != nil {
			_ = rows.Close()
			return ArtifactCleanupReport{}, err
		}
		expected[filepath.Base(filepath.FromSlash(relative))] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return ArtifactCleanupReport{}, fmt.Errorf("close registered artifacts: %w", err)
	}
	if err := rows.Err(); err != nil {
		return ArtifactCleanupReport{}, fmt.Errorf("iterate registered artifacts: %w", err)
	}
	entries, err := os.ReadDir(s.artifactDir)
	if err != nil {
		return ArtifactCleanupReport{}, fmt.Errorf("scan artifact directory: %w", err)
	}
	report := ArtifactCleanupReport{}
	for _, entry := range entries {
		name := entry.Name()
		_, registered := expected[name]
		managed := strings.HasPrefix(name, ".tmp-artifact-") ||
			(strings.HasPrefix(name, "art_") && strings.HasSuffix(name, ".json"))
		if registered || !managed {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return report, fmt.Errorf("inspect orphan artifact %s: %w", name, err)
		}
		if s.now().Sub(info.ModTime()) < artifactOrphanGracePeriod {
			continue
		}
		full := filepath.Join(s.artifactDir, name)
		if err := os.Remove(full); err != nil {
			return report, fmt.Errorf("remove orphan artifact %s: %w", name, err)
		}
		report.Removed = append(report.Removed, filepath.ToSlash(filepath.Join(artifactDirectoryName, name)))
	}
	return report, nil
}
