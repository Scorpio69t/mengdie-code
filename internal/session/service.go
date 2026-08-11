// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultSessionListLimit = 100

type SessionSummary struct {
	ID              string    `json:"id"`
	ProjectRoot     string    `json:"project_root"`
	ProjectIdentity string    `json:"project_identity"`
	Title           string    `json:"title,omitempty"`
	Status          string    `json:"status"`
	LastSeq         uint64    `json:"last_seq"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ListOptions struct {
	ProjectRoot string
	AllProjects bool
	Limit       int
}

// Service is the application-facing session boundary; CLI/TUI callers never
// use SQL, cache, or notification-bus details directly.
type Service struct {
	store   *SQLiteStore
	factBus *PublicFactBus
}

type ServiceOption func(*Service) error

func WithPublicFactBus(bus *PublicFactBus) ServiceOption {
	return func(service *Service) error {
		if bus == nil {
			return errors.New("session service public fact bus is required")
		}
		service.factBus = bus
		return nil
	}
}

func NewService(store *SQLiteStore, options ...ServiceOption) (*Service, error) {
	if store == nil {
		return nil, errors.New("session service store is required")
	}
	service := &Service{store: store}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("session service option is required")
		}
		if err := option(service); err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (s *Service) ReplayPublicFacts(ctx context.Context, sessionID string, afterSeq uint64, limit int) (PublicFactPage, error) {
	if limit == 0 {
		limit = defaultLoadLimit
	}
	if limit < 1 || limit > maximumLoadLimit {
		return PublicFactPage{}, fmt.Errorf("public fact replay limit must be between 1 and %d", maximumLoadLimit)
	}
	records, err := s.store.Load(ctx, sessionID, afterSeq, limit)
	if err != nil {
		return PublicFactPage{}, err
	}
	page := PublicFactPage{Facts: make([]PublicFact, 0, len(records)), ThroughSeq: afterSeq, More: len(records) == limit}
	for _, record := range records {
		page.ThroughSeq = record.SessionSeq
		if fact, public := publicFactFromRecord(record); public {
			page.Facts = append(page.Facts, fact)
		}
	}
	return page, nil
}

func (s *Service) SubscribePublicFacts(sessionID string, afterSeq uint64) (PublicFactSubscription, error) {
	if s.factBus == nil {
		return nil, errors.New("session service public fact subscription is unavailable")
	}
	return s.factBus.Subscribe(sessionID, afterSeq)
}

func (s *Service) List(ctx context.Context, options ListOptions) (result []SessionSummary, resultErr error) {
	limit := options.Limit
	if limit == 0 {
		limit = defaultSessionListLimit
	}
	if limit < 1 || limit > maximumLoadLimit {
		return nil, fmt.Errorf("session list limit must be between 1 and %d", maximumLoadLimit)
	}
	query := `SELECT id, project_root, project_identity, COALESCE(title,''), status, last_seq, created_at, updated_at FROM sessions`
	args := []any{}
	if !options.AllProjects {
		root := strings.TrimSpace(options.ProjectRoot)
		if root == "" {
			return nil, errors.New("session list project root is required without all-projects")
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve session list project root: %w", err)
		}
		query += ` WHERE project_identity=?`
		args = append(args, projectIdentity(filepath.Clean(absolute)))
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, classifySQLiteError("list sessions", err)
	}
	defer closeRows(rows, &resultErr, "close session list rows")
	result = make([]SessionSummary, 0)
	for rows.Next() {
		var item SessionSummary
		var createdAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.ProjectRoot, &item.ProjectIdentity, &item.Title, &item.Status, &item.LastSeq, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan session summary: %w", err)
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("decode session created time: %w", err)
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("decode session updated time: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, classifySQLiteError("iterate sessions", err)
	}
	return result, nil
}

func (s *Service) View(ctx context.Context, sessionID string) (SessionView, error) {
	base, err := s.loadBase(ctx, sessionID)
	if err != nil {
		return SessionView{}, err
	}
	afterSeq := uint64(0)
	snapshot, err := s.store.LoadSnapshot(ctx, sessionID)
	switch {
	case err == nil:
		base, afterSeq = snapshot.State, snapshot.ThroughSeq
	case errors.Is(err, ErrSnapshotNotFound):
	case errors.Is(err, ErrSnapshotCorrupt), errors.Is(err, ErrSnapshotIncompatible):
		if deleteErr := s.store.DeleteSnapshot(ctx, sessionID); deleteErr != nil {
			return SessionView{}, errors.Join(err, fmt.Errorf("discard invalid snapshot: %w", deleteErr))
		}
	default:
		return SessionView{}, err
	}
	view, err := s.reduceAll(ctx, base, afterSeq)
	if err != nil {
		return SessionView{}, err
	}
	normalizeSessionView(&view)
	return view, nil
}

func (s *Service) RefreshSnapshot(ctx context.Context, sessionID string) error {
	expected := uint64(0)
	if snapshot, err := s.store.LoadSnapshot(ctx, sessionID); err == nil {
		expected = snapshot.ThroughSeq
	} else if !errors.Is(err, ErrSnapshotNotFound) && !errors.Is(err, ErrSnapshotCorrupt) && !errors.Is(err, ErrSnapshotIncompatible) {
		return err
	} else if !errors.Is(err, ErrSnapshotNotFound) {
		if err := s.store.DeleteSnapshot(ctx, sessionID); err != nil {
			return err
		}
	}
	view, err := s.View(ctx, sessionID)
	if err != nil {
		return err
	}
	return s.store.SaveSnapshot(ctx, view, expected)
}

func (s *Service) Delete(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session id is required")
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteError("begin session delete", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT relative_path FROM artifacts WHERE session_id=? AND deleted_at IS NULL`, sessionID)
	if err != nil {
		return classifySQLiteError("list session artifacts", err)
	}
	paths := make([]string, 0)
	for rows.Next() {
		var relative string
		if err := rows.Scan(&relative); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan session artifact: %w", err)
		}
		paths = append(paths, relative)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close session artifacts: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate session artifacts: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, sessionID)
	if err != nil {
		return classifySQLiteError("delete session", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect session delete: %w", err)
	}
	if count == 0 {
		return ErrSessionNotFound
	}
	if err := tx.Commit(); err != nil {
		return classifySQLiteError("commit session delete", err)
	}
	var cleanupErr error
	remaining := make([]string, 0)
	for _, relative := range paths {
		full, err := s.store.resolveArtifactPath(relative)
		if err == nil {
			err = os.Remove(full)
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			remaining = append(remaining, relative)
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if cleanupErr != nil {
		return &ArtifactCleanupError{Paths: remaining, Err: cleanupErr}
	}
	return nil
}

func (s *Service) loadBase(ctx context.Context, sessionID string) (SessionView, error) {
	var view SessionView
	var createdAt, updatedAt string
	err := s.store.db.QueryRowContext(ctx, `
SELECT id, project_root, project_identity, COALESCE(title,''), status, created_at, updated_at
FROM sessions WHERE id=?`, sessionID).Scan(
		&view.ID, &view.ProjectRoot, &view.ProjectIdentity, &view.Title, &view.Status, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionView{}, ErrSessionNotFound
	}
	if err != nil {
		return SessionView{}, classifySQLiteError("load session", err)
	}
	view.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return SessionView{}, fmt.Errorf("decode session created time: %w", err)
	}
	view.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return SessionView{}, fmt.Errorf("decode session updated time: %w", err)
	}
	normalizeSessionView(&view)
	return view, nil
}

func (s *Service) reduceAll(ctx context.Context, base SessionView, afterSeq uint64) (SessionView, error) {
	view := base
	for {
		records, err := s.store.Load(ctx, view.ID, afterSeq, maximumLoadLimit)
		if err != nil {
			return SessionView{}, err
		}
		if len(records) == 0 {
			return view, nil
		}
		view, err = Reduce(view, records)
		if err != nil {
			return SessionView{}, err
		}
		afterSeq = records[len(records)-1].SessionSeq
		if len(records) < maximumLoadLimit {
			return view, nil
		}
	}
}
