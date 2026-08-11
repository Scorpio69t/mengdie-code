// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

type PatchStatus string

const (
	PatchPrepared PatchStatus = "prepared"
	PatchApplied  PatchStatus = "applied"
	PatchVerified PatchStatus = "verified"
	PatchRewound  PatchStatus = "rewound"
	PatchConflict PatchStatus = "conflict"
	PatchAborted  PatchStatus = "aborted"
)

var (
	ErrPatchJournalNotFound = errors.New("patch journal not found")
	ErrPatchJournalConflict = errors.New("patch journal state conflict")
)

// PatchRecovery is the durable decision for one previously incomplete write.
// It intentionally exposes identities and state only, never paths or content.
type PatchRecovery struct {
	JournalID   string
	RunID       string
	ToolCallID  string
	ToolName    string
	CallDigest  string
	Status      PatchStatus
	ConflictMsg string
}

// PatchJournalRecorder binds write facts to exactly one Session/Run/Command.
// It carries no Capability and cannot authorize a mutation.
type PatchJournalRecorder struct {
	store       *SQLiteStore
	sessionID   string
	runID       string
	commandID   string
	projectRoot string
}

type patchJournalEntry struct {
	JournalID       string
	RunID           string
	ToolCallID      string
	ToolName        string
	CallDigest      string
	Status          PatchStatus
	ProjectRoot     string
	RelativePath    string
	PathFingerprint string
	PreExists       bool
	PreSHA256       string
	PreMode         uint32
	PostExists      bool
	PostSHA256      string
	PostMode        uint32
}

type patchObservation struct {
	exists      bool
	sha256      string
	mode        uint32
	fingerprint string
}

func (s *SQLiteStore) NewPatchJournalRecorder(ctx context.Context, sessionID, runID, commandID, projectRoot string) (*PatchJournalRecorder, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(runID) == "" || strings.TrimSpace(commandID) == "" {
		return nil, errors.New("patch journal identities are required")
	}
	requestedGuard, err := platform.NewPathGuard(strings.TrimSpace(projectRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve patch journal project root: %w", err)
	}
	root := requestedGuard.Root()
	var persistedRoot string
	err = s.db.QueryRowContext(ctx, `
SELECT se.project_root
FROM runs r JOIN sessions se ON se.id=r.session_id
WHERE r.id=? AND r.session_id=? AND r.command_id=? AND r.status='running'`, runID, sessionID, commandID).Scan(&persistedRoot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunConflict
	}
	if err != nil {
		return nil, classifySQLiteError("verify patch journal identity", err)
	}
	persistedGuard, err := platform.NewPathGuard(persistedRoot)
	if err != nil || !sameCanonicalPath(root, persistedGuard.Root()) {
		return nil, ErrRunConflict
	}
	return &PatchJournalRecorder{
		store: s, sessionID: sessionID, runID: runID, commandID: commandID, projectRoot: root,
	}, nil
}

// Prepare implements tools.MutationJournal. The transaction commits before
// the write tool is allowed to create directories, staging files, or targets.
func (r *PatchJournalRecorder) Prepare(ctx context.Context, intent tools.MutationIntent) (result tools.MutationReceipt, resultErr error) {
	relative, fingerprint, err := validateMutationIntent(r.projectRoot, intent)
	if err != nil {
		return tools.MutationReceipt{}, err
	}
	journalID := patchIdentity("jnl_", r.sessionID, r.runID, intent.ToolCallID, intent.CallDigest)
	entryID := patchIdentity("pte_", journalID, relative)
	stamp := formatTime(r.store.now().UTC())
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return tools.MutationReceipt{}, classifySQLiteError("begin patch journal prepare", err)
	}
	defer rollbackTransaction(tx, &resultErr, "rollback patch journal prepare")
	if _, err := tx.ExecContext(ctx, `
INSERT INTO patch_journals(
    id, session_id, run_id, command_id, tool_call_id, tool_name, call_digest,
    status, conflict_reason, prepared_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 'prepared', NULL, ?)`,
		journalID, r.sessionID, r.runID, r.commandID, intent.ToolCallID, intent.ToolName, intent.CallDigest, stamp,
	); err != nil {
		return tools.MutationReceipt{}, classifySQLiteError("insert patch journal", err)
	}
	var preSHA, preMode any
	if intent.PreExists {
		preSHA, preMode = intent.PreSHA256, int64(intent.PreMode.Perm())
	}
	var postSHA, postMode any
	if intent.PostExists {
		postSHA, postMode = intent.PostSHA256, int64(intent.PostMode.Perm())
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO patch_entries(
    id, journal_id, ordinal, relative_path, path_fingerprint,
    pre_existed, pre_sha256, pre_mode, post_existed, post_sha256, post_mode,
    status, conflict_reason
) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, 'prepared', NULL)`,
		entryID, journalID, relative, fingerprint, boolInt(intent.PreExists), preSHA, preMode,
		boolInt(intent.PostExists), postSHA, postMode,
	); err != nil {
		return tools.MutationReceipt{}, classifySQLiteError("insert patch entry", err)
	}
	if err := tx.Commit(); err != nil {
		return tools.MutationReceipt{}, classifySQLiteError("commit patch journal prepare", err)
	}
	return tools.MutationReceipt{JournalID: journalID}, nil
}

func (r *PatchJournalRecorder) MarkApplied(ctx context.Context, receipt tools.MutationReceipt) error {
	return r.store.transitionPatchJournal(ctx, receipt.JournalID, PatchApplied, "")
}

func (r *PatchJournalRecorder) VerifyPost(ctx context.Context, receipt tools.MutationReceipt) error {
	entry, err := r.store.loadPatchJournal(ctx, receipt.JournalID)
	if err != nil {
		return err
	}
	observation, reason := observePatchPath(entry)
	if reason == "" && matchesPatchState(observation, entry.PostExists, entry.PostSHA256) {
		return r.store.transitionPatchJournal(ctx, receipt.JournalID, PatchVerified, "")
	}
	if reason == "" {
		reason = "当前文件不匹配 Journal 写后哈希"
	}
	if err := r.store.transitionPatchJournal(ctx, receipt.JournalID, PatchConflict, reason); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", tools.ErrMutationConflict, reason)
}

// RecoverPatchJournals resolves every incomplete write for one Session using
// only the current guarded path and the durable pre/post facts.
func (s *SQLiteStore) RecoverPatchJournals(ctx context.Context, sessionID, projectRoot string) ([]PatchRecovery, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("patch recovery session id is required")
	}
	root, err := filepath.Abs(strings.TrimSpace(projectRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve patch recovery project root: %w", err)
	}
	var persistedRoot string
	if err := s.db.QueryRowContext(ctx, `SELECT project_root FROM sessions WHERE id=?`, sessionID).Scan(&persistedRoot); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, classifySQLiteError("load patch recovery session", err)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id FROM patch_journals
WHERE session_id=? AND status IN ('prepared','applied')
ORDER BY prepared_at, id`, sessionID)
	if err != nil {
		return nil, classifySQLiteError("list incomplete patch journals", err)
	}
	var journalIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan incomplete patch journal: %w", err)
		}
		journalIDs = append(journalIDs, id)
	}
	if closeErr := rows.Close(); closeErr != nil {
		return nil, fmt.Errorf("close incomplete patch journal rows: %w", closeErr)
	}
	if err := rows.Err(); err != nil {
		return nil, classifySQLiteError("iterate incomplete patch journals", err)
	}
	if len(journalIDs) == 0 {
		return s.listPatchRecoveries(ctx, sessionID)
	}
	requestedGuard, err := platform.NewPathGuard(root)
	if err != nil {
		return nil, ErrRunConflict
	}
	persistedGuard, err := platform.NewPathGuard(persistedRoot)
	if err != nil || !sameCanonicalPath(requestedGuard.Root(), persistedGuard.Root()) {
		return nil, ErrRunConflict
	}
	for _, id := range journalIDs {
		entry, err := s.loadPatchJournal(ctx, id)
		if err != nil {
			return nil, err
		}
		observation, reason := observePatchPath(entry)
		status := PatchConflict
		switch {
		case reason != "":
		case matchesPatchState(observation, entry.PostExists, entry.PostSHA256):
			status = PatchVerified
		case matchesPatchState(observation, entry.PreExists, entry.PreSHA256):
			status = PatchAborted
		default:
			reason = "当前文件既不匹配 Journal 写前哈希，也不匹配写后哈希"
		}
		if err := s.transitionPatchJournal(ctx, id, status, reason); err != nil {
			return nil, err
		}
	}
	return s.listPatchRecoveries(ctx, sessionID)
}

func (s *SQLiteStore) listPatchRecoveries(ctx context.Context, sessionID string) (result []PatchRecovery, resultErr error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, tool_call_id, tool_name, call_digest, status, COALESCE(conflict_reason, '')
FROM patch_journals WHERE session_id=? ORDER BY prepared_at, id`, sessionID)
	if err != nil {
		return nil, classifySQLiteError("list patch recovery decisions", err)
	}
	defer closeRows(rows, &resultErr, "close patch recovery decision rows")
	for rows.Next() {
		var item PatchRecovery
		if err := rows.Scan(
			&item.JournalID, &item.RunID, &item.ToolCallID, &item.ToolName,
			&item.CallDigest, &item.Status, &item.ConflictMsg,
		); err != nil {
			return nil, fmt.Errorf("scan patch recovery decision: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, classifySQLiteError("iterate patch recovery decisions", err)
	}
	return result, nil
}

func (s *SQLiteStore) loadPatchJournal(ctx context.Context, journalID string) (patchJournalEntry, error) {
	if !validPatchIdentity(journalID, "jnl_") {
		return patchJournalEntry{}, errors.New("patch journal id is invalid")
	}
	var item patchJournalEntry
	var entryStatus PatchStatus
	var preExists, postExists int
	var preSHA, postSHA sql.NullString
	var preMode, postMode sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
SELECT j.id, j.run_id, j.tool_call_id, j.tool_name, j.call_digest, j.status, e.status,
       se.project_root, e.relative_path, e.path_fingerprint,
       e.pre_existed, e.pre_sha256, e.pre_mode,
       e.post_existed, e.post_sha256, e.post_mode
FROM patch_journals j
JOIN sessions se ON se.id=j.session_id
JOIN patch_entries e ON e.journal_id=j.id AND e.ordinal=1
WHERE j.id=?`, journalID).Scan(
		&item.JournalID, &item.RunID, &item.ToolCallID, &item.ToolName, &item.CallDigest, &item.Status, &entryStatus,
		&item.ProjectRoot, &item.RelativePath, &item.PathFingerprint,
		&preExists, &preSHA, &preMode, &postExists, &postSHA, &postMode,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return patchJournalEntry{}, ErrPatchJournalNotFound
	}
	if err != nil {
		return patchJournalEntry{}, classifySQLiteError("load patch journal", err)
	}
	item.PreExists, item.PostExists = preExists == 1, postExists == 1
	if preSHA.Valid {
		item.PreSHA256 = preSHA.String
	}
	if postSHA.Valid {
		item.PostSHA256 = postSHA.String
	}
	if preMode.Valid {
		item.PreMode = uint32(preMode.Int64)
	}
	if postMode.Valid {
		item.PostMode = uint32(postMode.Int64)
	}
	if entryStatus != item.Status || !validHexSHA256(item.PathFingerprint) ||
		item.PreExists != (item.PreSHA256 != "") || item.PostExists != (item.PostSHA256 != "") ||
		(item.PreExists && !validHexSHA256(item.PreSHA256)) || (item.PostExists && !validHexSHA256(item.PostSHA256)) {
		return patchJournalEntry{}, fmt.Errorf("%w: persisted entry is inconsistent", ErrPatchJournalConflict)
	}
	return item, nil
}

func (s *SQLiteStore) transitionPatchJournal(ctx context.Context, journalID string, status PatchStatus, reason string) (resultErr error) {
	if !validPatchIdentity(journalID, "jnl_") {
		return errors.New("patch journal id is invalid")
	}
	if len(reason) > 1024 {
		return errors.New("patch journal conflict reason exceeds 1024 bytes")
	}
	stamp := formatTime(s.now().UTC())
	setClause := "status=?"
	args := []any{status}
	switch status {
	case PatchApplied:
		setClause += ", conflict_reason=NULL, applied_at=?"
		args = append(args, stamp)
	case PatchVerified:
		setClause += ", conflict_reason=NULL, verified_at=?, resolved_at=?"
		args = append(args, stamp, stamp)
	case PatchConflict, PatchAborted:
		setClause += ", conflict_reason=?, resolved_at=?"
		args = append(args, nullableReason(reason), stamp)
	default:
		return fmt.Errorf("unsupported patch transition %q", status)
	}
	args = append(args, journalID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteError("begin patch journal transition", err)
	}
	defer rollbackTransaction(tx, &resultErr, "rollback patch journal transition")
	query := `UPDATE patch_journals SET ` + setClause + ` WHERE id=? AND status IN ('prepared','applied')`
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return classifySQLiteError("transition patch journal", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect patch journal transition: %w", err)
	}
	if count != 1 {
		var actual PatchStatus
		if err := tx.QueryRowContext(ctx, `SELECT status FROM patch_journals WHERE id=?`, journalID).Scan(&actual); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrPatchJournalNotFound
			}
			return classifySQLiteError("load patch journal transition state", err)
		}
		if actual == status {
			if err := tx.Commit(); err != nil {
				return classifySQLiteError("commit idempotent patch journal transition", err)
			}
			return nil
		}
		return fmt.Errorf("%w: journal=%s actual=%s want=%s", ErrPatchJournalConflict, journalID, actual, status)
	}
	entryQuery := `UPDATE patch_entries SET ` + setClause + ` WHERE journal_id=? AND ordinal=1 AND status IN ('prepared','applied')`
	entryResult, err := tx.ExecContext(ctx, entryQuery, args...)
	if err != nil {
		return classifySQLiteError("transition patch entry", err)
	}
	entryCount, err := entryResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect patch entry transition: %w", err)
	}
	if entryCount != 1 {
		return fmt.Errorf("%w: patch entry missing for journal %s", ErrPatchJournalConflict, journalID)
	}
	if err := tx.Commit(); err != nil {
		return classifySQLiteError("commit patch journal transition", err)
	}
	return nil
}

func validateMutationIntent(projectRoot string, intent tools.MutationIntent) (string, string, error) {
	if intent.ToolName != "edit_file" && intent.ToolName != "write_file" {
		return "", "", errors.New("patch journal supports only edit_file and write_file")
	}
	if strings.TrimSpace(intent.ToolCallID) == "" || len(intent.ToolCallID) > 256 {
		return "", "", errors.New("patch journal tool call id is invalid")
	}
	if !validHexSHA256(intent.CallDigest) {
		return "", "", errors.New("patch journal call digest is invalid")
	}
	if !filepath.IsAbs(intent.Path) || filepath.Clean(intent.Path) != intent.Path {
		return "", "", errors.New("patch journal path must be canonical and absolute")
	}
	relative, err := filepath.Rel(projectRoot, intent.Path)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("patch journal path is outside the project root")
	}
	if intent.PreExists != (intent.PreSHA256 != "") || intent.PostExists != (intent.PostSHA256 != "") {
		return "", "", errors.New("patch journal existence and hashes are inconsistent")
	}
	if intent.PreExists && !validHexSHA256(intent.PreSHA256) {
		return "", "", errors.New("patch journal pre hash is invalid")
	}
	if intent.PostExists && !validHexSHA256(intent.PostSHA256) {
		return "", "", errors.New("patch journal post hash is invalid")
	}
	if !intent.PostExists {
		return "", "", errors.New("current write tools must produce an existing post-state")
	}
	if intent.PreExists && intent.PreSHA256 == intent.PostSHA256 {
		return "", "", errors.New("patch journal refuses an indistinguishable pre/post state")
	}
	return filepath.ToSlash(relative), canonicalPathFingerprint(intent.Path), nil
}

func observePatchPath(entry patchJournalEntry) (patchObservation, string) {
	guard, err := platform.NewPathGuard(entry.ProjectRoot)
	if err != nil {
		return patchObservation{}, "项目根目录无法重新验证"
	}
	resolved, err := guard.Resolve(filepath.FromSlash(entry.RelativePath), platform.AccessWrite)
	if err != nil {
		return patchObservation{}, "目标路径无法在项目边界内重新解析"
	}
	observation := patchObservation{fingerprint: canonicalPathFingerprint(resolved.Path)}
	if observation.fingerprint != entry.PathFingerprint {
		return observation, "目标路径身份已发生变化"
	}
	info, err := os.Stat(resolved.Path)
	if errors.Is(err, os.ErrNotExist) {
		return observation, ""
	}
	if err != nil {
		return observation, "目标文件状态无法读取"
	}
	if !info.Mode().IsRegular() {
		return observation, "目标路径不再是普通文件"
	}
	digest, err := tools.FileSHA256(resolved.Path)
	if err != nil {
		return observation, "目标文件哈希无法读取"
	}
	observation.exists, observation.sha256, observation.mode = true, digest, uint32(info.Mode().Perm())
	return observation, ""
}

func matchesPatchState(observation patchObservation, exists bool, sha256 string) bool {
	if observation.exists != exists {
		return false
	}
	return !exists || observation.sha256 == sha256
}

func patchIdentity(prefix string, parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return prefix + hex.EncodeToString(digest[:])
}

func validPatchIdentity(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) == len(prefix)+64 && validHexSHA256(value[len(prefix):])
}

func canonicalPathFingerprint(path string) string {
	canonical := filepath.ToSlash(filepath.Clean(path))
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

func sameCanonicalPath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func validHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableReason(reason string) any {
	if strings.TrimSpace(reason) == "" {
		return nil
	}
	return reason
}

var _ tools.MutationJournal = (*PatchJournalRecorder)(nil)
