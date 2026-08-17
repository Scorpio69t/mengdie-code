// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

var (
	ErrRewindUnavailable = errors.New("patch journal is not safely rewindable")
	ErrRewindConflict    = errors.New("patch rewind conflicts with current project state")
)

type BeginRewindResult struct {
	Command  Command
	Existing bool
}

// ResolveRewindJournal returns the newest verified, unclaimed Journal for the
// requested Session. InspectRewind remains the authority for current-state
// validation; this method only supplies the application default target.
func (s *SQLiteStore) ResolveRewindJournal(ctx context.Context, sessionID, projectRoot string) (string, error) {
	guard, err := platform.NewPathGuard(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve rewind project root: %w", err)
	}
	var journalID string
	err = s.db.QueryRowContext(ctx, `
SELECT j.id
FROM patch_journals j
JOIN sessions s ON s.id=j.session_id
WHERE j.session_id=? AND s.project_identity=?
  AND j.status='verified' AND j.rewind_command_id IS NULL
ORDER BY j.verified_at DESC, j.prepared_at DESC, j.id DESC
LIMIT 1`, sessionID, projectIdentity(guard.Root())).Scan(&journalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrRewindUnavailable
	}
	if err != nil {
		return "", classifySQLiteError("resolve rewind journal", err)
	}
	return journalID, nil
}

// BeginRewindCommand registers an idempotent local intent before approval or
// project mutation. Repeated IDs never execute another rewind.
func (s *SQLiteStore) BeginRewindCommand(ctx context.Context, sessionID, journalID, commandID, projectRoot string) (result BeginRewindResult, resultErr error) {
	payload, err := RewindCommandPayload(sessionID, journalID)
	if err != nil {
		return BeginRewindResult{}, err
	}
	if strings.TrimSpace(commandID) == "" || len(commandID) > 128 {
		return BeginRewindResult{}, errors.New("rewind command id is invalid")
	}
	digest, err := commandPayloadDigest(payload)
	if err != nil {
		return BeginRewindResult{}, err
	}
	guard, err := platform.NewPathGuard(projectRoot)
	if err != nil {
		return BeginRewindResult{}, fmt.Errorf("resolve rewind project root: %w", err)
	}
	identity := projectIdentity(guard.Root())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BeginRewindResult{}, classifySQLiteError("begin rewind command", err)
	}
	defer rollbackTransaction(tx, &resultErr, "rollback rewind command")
	existing, _, existingProject, err := loadCommandTx(ctx, tx, commandID)
	if err == nil {
		if existing.SessionID != sessionID || existing.Kind != CommandKindRewind ||
			existing.PayloadSHA256 != digest || existingProject != identity {
			return BeginRewindResult{}, ErrCommandConflict
		}
		if err := tx.Commit(); err != nil {
			return BeginRewindResult{}, classifySQLiteError("commit existing rewind command", err)
		}
		return BeginRewindResult{Command: existing, Existing: true}, nil
	}
	if !errors.Is(err, ErrCommandNotFound) {
		return BeginRewindResult{}, err
	}
	var persistedProject string
	if err := tx.QueryRowContext(ctx, `SELECT project_identity FROM sessions WHERE id=?`, sessionID).Scan(&persistedProject); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BeginRewindResult{}, ErrSessionNotFound
		}
		return BeginRewindResult{}, classifySQLiteError("load rewind session", err)
	}
	if persistedProject != identity {
		return BeginRewindResult{}, ErrRunConflict
	}
	var journalStatus PatchStatus
	var journalSession string
	var rewindCommand sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT session_id, status, rewind_command_id FROM patch_journals WHERE id=?`, journalID).Scan(
		&journalSession, &journalStatus, &rewindCommand,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BeginRewindResult{}, ErrPatchJournalNotFound
		}
		return BeginRewindResult{}, classifySQLiteError("load rewind journal", err)
	}
	if journalSession != sessionID || journalStatus != PatchVerified || rewindCommand.Valid {
		return BeginRewindResult{}, ErrRewindUnavailable
	}
	stamp := formatTime(s.now().UTC())
	if _, err := tx.ExecContext(ctx, `
INSERT INTO commands(id, session_id, kind, payload_json, payload_sha256, status, result_seq, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 'accepted', NULL, ?, ?)`,
		commandID, sessionID, CommandKindRewind, []byte(payload), digest, stamp, stamp,
	); err != nil {
		return BeginRewindResult{}, classifySQLiteError("insert rewind command", err)
	}
	if err := tx.Commit(); err != nil {
		return BeginRewindResult{}, classifySQLiteError("commit rewind command", err)
	}
	now := s.now().UTC()
	return BeginRewindResult{Command: Command{
		ID: commandID, SessionID: sessionID, Kind: CommandKindRewind,
		Payload: payload, PayloadSHA256: digest, Status: CommandAccepted,
		CreatedAt: now, UpdatedAt: now,
	}}, nil
}

// InspectRewind implements tools.RewindBackend. It validates the current
// post-state and loads the private preimage through the existing Artifact
// integrity and quota boundary.
func (s *SQLiteStore) InspectRewind(ctx context.Context, sessionID, journalID, projectRoot string) (tools.RewindTarget, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(journalID) == "" {
		return tools.RewindTarget{}, errors.New("rewind session and journal identities are required")
	}
	entry, err := s.loadPatchJournal(ctx, journalID)
	if err != nil {
		return tools.RewindTarget{}, err
	}
	if entry.SessionID != sessionID || entry.Status != PatchVerified || entry.RewindCommandID != "" {
		return tools.RewindTarget{}, ErrRewindUnavailable
	}
	requestedGuard, err := platform.NewPathGuard(projectRoot)
	if err != nil {
		return tools.RewindTarget{}, fmt.Errorf("resolve rewind project root: %w", err)
	}
	persistedGuard, err := platform.NewPathGuard(entry.ProjectRoot)
	if err != nil || !sameCanonicalPath(requestedGuard.Root(), persistedGuard.Root()) {
		return tools.RewindTarget{}, ErrRunConflict
	}
	resolved, err := persistedGuard.Resolve(filepath.FromSlash(entry.RelativePath), platform.AccessWrite)
	if err != nil || canonicalPathFingerprint(resolved.Path) != entry.PathFingerprint {
		return tools.RewindTarget{}, fmt.Errorf("%w: 目标路径身份已发生变化", ErrRewindConflict)
	}
	observation, reason := observePatchPath(entry)
	if reason != "" || !matchesPatchStateAndMode(observation, entry.PostExists, entry.PostSHA256, entry.PostMode) {
		if reason == "" {
			reason = "当前文件内容或权限不再匹配 Journal 写后状态"
		}
		return tools.RewindTarget{}, fmt.Errorf("%w: %s", ErrRewindConflict, reason)
	}
	postContent, err := os.ReadFile(resolved.Path)
	if err != nil {
		return tools.RewindTarget{}, fmt.Errorf("read rewind post-state: %w", err)
	}
	postDigest := sha256.Sum256(postContent)
	postInfo, err := os.Stat(resolved.Path)
	if err != nil || !postInfo.Mode().IsRegular() || hex.EncodeToString(postDigest[:]) != entry.PostSHA256 ||
		postInfo.Mode().Perm() != os.FileMode(entry.PostMode).Perm() {
		return tools.RewindTarget{}, fmt.Errorf("%w: 目标在生成回滚预览时发生变化", ErrRewindConflict)
	}
	preContent, err := s.loadRewindPreimage(ctx, entry)
	if err != nil {
		return tools.RewindTarget{}, err
	}
	return tools.RewindTarget{
		SessionID: sessionID, JournalID: journalID, Path: resolved.Path, PathFingerprint: entry.PathFingerprint,
		PreExists: entry.PreExists, PreContent: preContent, PreSHA256: entry.PreSHA256, PreMode: os.FileMode(entry.PreMode),
		PostContent: postContent, PostSHA256: entry.PostSHA256, PostMode: os.FileMode(entry.PostMode),
	}, nil
}

func (s *SQLiteStore) loadRewindPreimage(ctx context.Context, entry patchJournalEntry) ([]byte, error) {
	if !entry.PreExists {
		if entry.ReverseInlineSet || entry.ReverseArtifact != "" || entry.ReverseSize != 0 || entry.ReverseSHA256 != "" {
			return nil, fmt.Errorf("%w: absent pre-state has rewind material", ErrPatchJournalConflict)
		}
		return nil, nil
	}
	if entry.ReverseSize < 0 || entry.ReverseSHA256 != entry.PreSHA256 ||
		entry.ReverseInlineSet == (entry.ReverseArtifact != "") {
		return nil, fmt.Errorf("%w: rewind material metadata is incomplete", ErrRewindUnavailable)
	}
	var content []byte
	if entry.ReverseInlineSet {
		content = append([]byte(nil), entry.ReverseInline...)
	} else {
		var item artifactMetadata
		var sensitivity string
		err := s.db.QueryRowContext(ctx, `
SELECT id, session_id, run_id, kind, mime, sensitivity, relative_path, sha256, size_bytes
FROM artifacts WHERE id=? AND deleted_at IS NULL`, entry.ReverseArtifact).Scan(
			&item.ID, &item.SessionID, &item.RunID, &item.Kind, &item.MIME, &sensitivity,
			&item.RelativePath, &item.SHA256, &item.SizeBytes,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrArtifactNotFound
		}
		if err != nil {
			return nil, classifySQLiteError("load rewind artifact", err)
		}
		item.Sensitivity = Visibility(sensitivity)
		if item.SessionID != entry.SessionID || item.RunID != entry.RunID || item.Kind != "patch-reverse" || item.Sensitivity != VisibilityPrivate {
			return nil, fmt.Errorf("%w: rewind artifact identity mismatch", ErrArtifactCorrupt)
		}
		content, err = s.readArtifactFile(item)
		if err != nil {
			return nil, err
		}
	}
	digest := sha256.Sum256(content)
	if int64(len(content)) != entry.ReverseSize || hex.EncodeToString(digest[:]) != entry.ReverseSHA256 {
		return nil, fmt.Errorf("%w: rewind material checksum mismatch", ErrArtifactCorrupt)
	}
	return content, nil
}

func (s *SQLiteStore) StartRewind(ctx context.Context, sessionID, journalID, commandID string) (resultErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteError("begin rewind start", err)
	}
	defer rollbackTransaction(tx, &resultErr, "rollback rewind start")
	stamp := formatTime(s.now().UTC())
	commandResult, err := tx.ExecContext(ctx, `
UPDATE commands SET status='running', updated_at=?
WHERE id=? AND session_id=? AND kind=? AND status='accepted'`,
		stamp, commandID, sessionID, CommandKindRewind,
	)
	if err != nil {
		return classifySQLiteError("start rewind command", err)
	}
	count, err := commandResult.RowsAffected()
	if err != nil || count != 1 {
		return ErrCommandConflict
	}
	journalResult, err := tx.ExecContext(ctx, `
UPDATE patch_journals
SET rewind_command_id=?, rewind_started_at=?, conflict_reason=NULL
WHERE id=? AND session_id=? AND status='verified' AND rewind_command_id IS NULL`,
		commandID, stamp, journalID, sessionID,
	)
	if err != nil {
		return classifySQLiteError("bind rewind journal", err)
	}
	count, err = journalResult.RowsAffected()
	if err != nil || count != 1 {
		return ErrRewindUnavailable
	}
	if err := tx.Commit(); err != nil {
		return classifySQLiteError("commit rewind start", err)
	}
	return nil
}

func (s *SQLiteStore) CompleteRewind(ctx context.Context, sessionID, journalID, commandID string) error {
	entry, err := s.loadPatchJournal(ctx, journalID)
	if err != nil {
		return err
	}
	if entry.SessionID != sessionID || entry.RewindCommandID != commandID {
		return ErrCommandConflict
	}
	observation, reason := observePatchPath(entry)
	if reason != "" || !matchesPatchStateAndMode(observation, entry.PreExists, entry.PreSHA256, entry.PreMode) {
		if reason == "" {
			reason = "rewind 后文件未严格匹配 Journal 写前状态"
		}
		return fmt.Errorf("%w: %s", ErrRewindConflict, reason)
	}
	return s.commitRewindOutcome(ctx, entry, commandID, CommandApplied, PatchRewound, "", false)
}

// RecoverRewindCommand classifies a previously started local rewind without
// replaying it. A pre-state finalizes success; a post-state records an
// interruption and releases the Journal for a new Command; anything else is
// a conflict that remains blocked.
func (s *SQLiteStore) RecoverRewindCommand(ctx context.Context, commandID, projectRoot string) (Command, error) {
	command, err := s.LookupCommand(ctx, commandID)
	if err != nil {
		return Command{}, err
	}
	if command.Kind != CommandKindRewind {
		return Command{}, ErrCommandConflict
	}
	if command.Status != CommandAccepted && command.Status != CommandRunning {
		return command, nil
	}
	var payload struct {
		SessionID string `json:"session_id"`
		JournalID string `json:"journal_id"`
	}
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return Command{}, fmt.Errorf("decode rewind command payload: %w", err)
	}
	entry, err := s.loadPatchJournal(ctx, payload.JournalID)
	if err != nil {
		return Command{}, err
	}
	requestedGuard, err := platform.NewPathGuard(projectRoot)
	if err != nil {
		return Command{}, err
	}
	persistedGuard, err := platform.NewPathGuard(entry.ProjectRoot)
	if err != nil || !sameCanonicalPath(requestedGuard.Root(), persistedGuard.Root()) || entry.SessionID != payload.SessionID {
		return Command{}, ErrRunConflict
	}
	if command.Status == CommandAccepted || entry.RewindCommandID == "" {
		if err := s.setRewindCommandStatus(ctx, command.ID, CommandAccepted, CommandInterrupted); err != nil {
			return Command{}, err
		}
		command.Status = CommandInterrupted
		return command, nil
	}
	if entry.RewindCommandID != command.ID {
		return Command{}, ErrCommandConflict
	}
	observation, reason := observePatchPath(entry)
	switch {
	case reason == "" && matchesPatchStateAndMode(observation, entry.PreExists, entry.PreSHA256, entry.PreMode):
		if err := s.commitRewindOutcome(ctx, entry, command.ID, CommandApplied, PatchRewound, "", false); err != nil {
			return Command{}, err
		}
		command.Status = CommandApplied
	case reason == "" && matchesPatchStateAndMode(observation, entry.PostExists, entry.PostSHA256, entry.PostMode):
		if err := s.commitRewindOutcome(ctx, entry, command.ID, CommandInterrupted, PatchVerified, "rewind 在文件副作用前中断", true); err != nil {
			return Command{}, err
		}
		command.Status = CommandInterrupted
	default:
		if reason == "" {
			reason = "当前文件既不匹配 rewind 前状态，也不匹配 rewind 后状态"
		}
		if err := s.commitRewindOutcome(ctx, entry, command.ID, CommandFailed, PatchConflict, reason, false); err != nil {
			return Command{}, err
		}
		command.Status = CommandFailed
	}
	return command, nil
}

func (s *SQLiteStore) RejectRewindCommand(ctx context.Context, commandID string) error {
	return s.setRewindCommandStatus(ctx, commandID, CommandAccepted, CommandRejected)
}

func (s *SQLiteStore) FailRewindCommand(ctx context.Context, commandID string) error {
	return s.setRewindCommandStatus(ctx, commandID, CommandAccepted, CommandFailed)
}

func (s *SQLiteStore) setRewindCommandStatus(ctx context.Context, commandID string, from, to CommandStatus) error {
	if to != CommandRejected && to != CommandFailed && to != CommandInterrupted {
		return errors.New("unsupported rewind command terminal status")
	}
	stamp := formatTime(s.now().UTC())
	result, err := s.db.ExecContext(ctx, `
UPDATE commands SET status=?, updated_at=?
WHERE id=? AND kind=? AND status=?`, to, stamp, commandID, CommandKindRewind, from)
	if err != nil {
		return classifySQLiteError("finish rewind command", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrCommandConflict
	}
	return nil
}

func (s *SQLiteStore) commitRewindOutcome(ctx context.Context, entry patchJournalEntry, commandID string, commandStatus CommandStatus, patchStatus PatchStatus, reason string, release bool) (resultErr error) {
	stamp := formatTime(s.now().UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteError("begin rewind outcome", err)
	}
	defer rollbackTransaction(tx, &resultErr, "rollback rewind outcome")
	journalSet := "status=?, conflict_reason=?, resolved_at=?"
	args := []any{patchStatus, nullableReason(reason), stamp}
	if patchStatus == PatchRewound {
		journalSet += ", rewound_at=?"
		args = append(args, stamp)
	}
	if release {
		journalSet += ", rewind_command_id=NULL, rewind_started_at=NULL"
	}
	args = append(args, entry.JournalID, commandID)
	result, err := tx.ExecContext(ctx, `UPDATE patch_journals SET `+journalSet+`
WHERE id=? AND status='verified' AND rewind_command_id=?`, args...)
	if err != nil {
		return classifySQLiteError("commit rewind journal outcome", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrPatchJournalConflict
	}
	entrySet := "status=?, conflict_reason=?, resolved_at=?"
	entryArgs := []any{patchStatus, nullableReason(reason), stamp}
	if patchStatus == PatchRewound {
		entrySet += ", rewound_at=?"
		entryArgs = append(entryArgs, stamp)
	}
	entryArgs = append(entryArgs, entry.JournalID)
	result, err = tx.ExecContext(ctx, `UPDATE patch_entries SET `+entrySet+`
WHERE journal_id=? AND ordinal=1 AND status='verified'`, entryArgs...)
	if err != nil {
		return classifySQLiteError("commit rewind entry outcome", err)
	}
	count, err = result.RowsAffected()
	if err != nil || count != 1 {
		return ErrPatchJournalConflict
	}
	result, err = tx.ExecContext(ctx, `UPDATE commands SET status=?, updated_at=?
WHERE id=? AND kind=? AND status='running'`, commandStatus, stamp, commandID, CommandKindRewind)
	if err != nil {
		return classifySQLiteError("commit rewind command outcome", err)
	}
	count, err = result.RowsAffected()
	if err != nil || count != 1 {
		return ErrCommandConflict
	}
	if err := tx.Commit(); err != nil {
		return classifySQLiteError("commit rewind outcome", err)
	}
	return nil
}

func matchesPatchStateAndMode(observation patchObservation, exists bool, sha256 string, mode uint32) bool {
	if !matchesPatchState(observation, exists, sha256) {
		return false
	}
	return !exists || observation.mode == mode
}

var _ tools.RewindBackend = (*SQLiteStore)(nil)
