// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// Service is the application-facing session boundary; CLI callers never use
// SQL or cache details directly.
type Service struct{ store *SQLiteStore }

func NewService(store *SQLiteStore) (*Service, error) {
	if store == nil {
		return nil, errors.New("session service store is required")
	}
	return &Service{store: store}, nil
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
	result, err := s.store.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, sessionID)
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
