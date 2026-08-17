// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

const rewindFileSchema = `{
  "type": "object",
  "properties": {
    "session_id": {"type": "string"},
    "journal_id": {"type": "string"}
  },
  "required": ["session_id", "journal_id"],
  "additionalProperties": false
}`

// RewindTarget is a private, integrity-checked preimage returned by the
// Session-owned backend. It must never be projected to public events.
type RewindTarget struct {
	SessionID       string
	JournalID       string
	Path            string
	PathFingerprint string
	PreExists       bool
	PreContent      []byte
	PreSHA256       string
	PreMode         fs.FileMode
	PostContent     []byte
	PostSHA256      string
	PostMode        fs.FileMode
}

// RewindBackend is defined by the tool-side consumer. Inspect is read-only;
// Start must durably bind the Command before a project mutation; Complete
// verifies and commits the rewound state after the mutation.
type RewindBackend interface {
	InspectRewind(context.Context, string, string, string) (RewindTarget, error)
	StartRewind(context.Context, string, string, string) error
	CompleteRewind(context.Context, string, string, string) error
}

type rewindFileArgs struct {
	SessionID string `json:"session_id"`
	JournalID string `json:"journal_id"`
}

func (args rewindFileArgs) validate() error {
	if strings.TrimSpace(args.SessionID) == "" || strings.TrimSpace(args.JournalID) == "" {
		return errors.New("rewind_file: session_id and journal_id are required")
	}
	return nil
}

type rewindPreparedArgs struct {
	SessionID       string `json:"session_id"`
	JournalID       string `json:"journal_id"`
	CommandID       string `json:"command_id"`
	Path            string `json:"path"`
	PathFingerprint string `json:"path_fingerprint"`
	PreExists       bool   `json:"pre_exists"`
	PreSHA256       string `json:"pre_sha256,omitempty"`
	PreMode         uint32 `json:"pre_mode,omitempty"`
	PostSHA256      string `json:"post_sha256"`
	PostMode        uint32 `json:"post_mode"`
}

// NewRewindFile builds the application-only rewind tool. It is intentionally
// not part of DefaultTools and therefore cannot be invoked by a Provider.
func NewRewindFile(backend RewindBackend, commandID string) (Tool, error) {
	if backend == nil {
		return nil, errors.New("rewind_file: backend is required")
	}
	if strings.TrimSpace(commandID) == "" {
		return nil, errors.New("rewind_file: command id is required")
	}
	return rewindFileTool{backend: backend, commandID: commandID}, nil
}

type rewindFileTool struct {
	backend   RewindBackend
	commandID string
}

func (rewindFileTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "rewind_file",
		Description: "在当前文件仍严格匹配 Journal 写后状态时恢复写前状态",
		InputSchema: json.RawMessage(rewindFileSchema),
		Effects:     []Effect{EffectWrite},
	}
}

func (t rewindFileTool) Prepare(ctx context.Context, raw json.RawMessage, env PrepareEnv) (*PreparedCall, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var args rewindFileArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if err := args.validate(); err != nil {
		return nil, err
	}
	target, err := t.backend.InspectRewind(ctx, args.SessionID, args.JournalID, env.Guard.Root())
	if err != nil {
		return nil, err
	}
	resolved, err := env.Guard.Resolve(target.Path, platform.AccessWrite)
	if err != nil {
		return nil, err
	}
	if resolved.Path != target.Path {
		return nil, errors.New("rewind_file: target path identity changed")
	}
	preview, err := focusedRewindDiff(target.Path, target.PostContent, target.PreContent, !target.PreExists)
	if err != nil {
		return nil, err
	}
	operation := "恢复"
	if !target.PreExists {
		operation = "删除由 Agent 创建的文件"
	}
	canonical, err := json.Marshal(rewindPreparedArgs{
		SessionID: target.SessionID, JournalID: target.JournalID, CommandID: t.commandID,
		Path: target.Path, PathFingerprint: target.PathFingerprint,
		PreExists: target.PreExists, PreSHA256: target.PreSHA256, PreMode: uint32(target.PreMode.Perm()),
		PostSHA256: target.PostSHA256, PostMode: uint32(target.PostMode.Perm()),
	})
	if err != nil {
		return nil, fmt.Errorf("rewind_file: encode prepared target: %w", err)
	}
	return PrepareCall(env.CallID, "rewind_file", canonical,
		[]Effect{EffectWrite}, []PathResource{{Path: target.Path}},
		Preview{Kind: PreviewDiff, Title: fmt.Sprintf("%s %s", operation, target.Path), Body: preview},
		[]Precondition{
			{Kind: PreconditionFileSHA256, Path: target.Path, SHA256: target.PostSHA256},
			{Kind: PreconditionFileMode, Path: target.Path, Mode: target.PostMode.Perm()},
		},
	)
}

func (t rewindFileTool) Execute(ctx context.Context, call *PreparedCall, cap Capability, env ExecEnv) (*ToolResult, error) {
	if err := CheckCapability(ctx, call, cap, env); err != nil {
		return nil, err
	}
	var prepared rewindPreparedArgs
	if err := decodeArgs(call.CanonicalArg, &prepared); err != nil {
		return nil, err
	}
	if prepared.CommandID != t.commandID {
		return nil, errors.New("rewind_file: command identity changed after approval")
	}
	target, err := t.backend.InspectRewind(ctx, prepared.SessionID, prepared.JournalID, env.Guard.Root())
	if err != nil {
		return nil, err
	}
	if !sameRewindTarget(prepared, target) {
		return nil, ErrMutationConflict
	}
	resolved, err := env.Guard.Resolve(target.Path, platform.AccessWrite)
	if err != nil {
		return nil, err
	}
	if err := ensureSamePreparedPath(call, resolved.Path); err != nil {
		return nil, err
	}
	if err := CheckPreconditions(call.Preconditions); err != nil {
		return nil, err
	}
	start := func() error {
		return t.backend.StartRewind(ctx, prepared.SessionID, prepared.JournalID, prepared.CommandID)
	}
	if target.PreExists {
		err = atomicWriteFile(env.Guard.Root(), target.Path, target.PreContent, target.PreMode, true, call.Preconditions, start)
	} else {
		err = atomicRemoveFile(env.Guard.Root(), target.Path, call.Preconditions, start)
	}
	if err != nil {
		return nil, fmt.Errorf("rewind_file: %w", err)
	}
	if err := t.backend.CompleteRewind(context.WithoutCancel(ctx), prepared.SessionID, prepared.JournalID, prepared.CommandID); err != nil {
		return nil, fmt.Errorf("rewind_file: record completed rewind: %w", err)
	}
	operation := "restored"
	if !target.PreExists {
		operation = "deleted"
	}
	return &ToolResult{
		Output: fmt.Sprintf("已安全撤销 %s", target.Path),
		Metadata: map[string]string{
			"journal_id": target.JournalID, "path": target.Path, "operation": operation,
		},
	}, nil
}

func sameRewindTarget(prepared rewindPreparedArgs, target RewindTarget) bool {
	if prepared.SessionID != target.SessionID || prepared.JournalID != target.JournalID ||
		prepared.Path != target.Path || prepared.PathFingerprint != target.PathFingerprint ||
		prepared.PreExists != target.PreExists || prepared.PreSHA256 != target.PreSHA256 ||
		prepared.PreMode != uint32(target.PreMode.Perm()) || prepared.PostSHA256 != target.PostSHA256 ||
		prepared.PostMode != uint32(target.PostMode.Perm()) {
		return false
	}
	if !target.PreExists {
		if len(target.PreContent) != 0 {
			return false
		}
	} else {
		digest := sha256.Sum256(target.PreContent)
		if hex.EncodeToString(digest[:]) != target.PreSHA256 {
			return false
		}
	}
	postDigest := sha256.Sum256(target.PostContent)
	return hex.EncodeToString(postDigest[:]) == target.PostSHA256
}

func focusedRewindDiff(path string, before, after []byte, deleting bool) (string, error) {
	var preview strings.Builder
	preview.WriteString("--- ")
	preview.WriteString(path)
	preview.WriteByte('\n')
	if deleting {
		preview.WriteString("+++ /dev/null\n@@ delete file @@\n")
		appendDiffText(&preview, '-', string(before))
	} else {
		preview.WriteString("+++ ")
		preview.WriteString(path)
		preview.WriteByte('\n')
		beforeLines, afterLines := strings.SplitAfter(string(before), "\n"), strings.SplitAfter(string(after), "\n")
		prefix := 0
		for prefix < len(beforeLines) && prefix < len(afterLines) && beforeLines[prefix] == afterLines[prefix] {
			prefix++
		}
		suffix := 0
		for suffix < len(beforeLines)-prefix && suffix < len(afterLines)-prefix &&
			beforeLines[len(beforeLines)-1-suffix] == afterLines[len(afterLines)-1-suffix] {
			suffix++
		}
		preview.WriteString(fmt.Sprintf("@@ rewind after line %d @@\n", prefix+1))
		for _, line := range beforeLines[prefix : len(beforeLines)-suffix] {
			appendDiffText(&preview, '-', line)
		}
		for _, line := range afterLines[prefix : len(afterLines)-suffix] {
			appendDiffText(&preview, '+', line)
		}
	}
	if preview.Len() > DefaultToolOutputBytes {
		return "", fmt.Errorf("rewind_file: diff preview exceeds %d bytes", DefaultToolOutputBytes)
	}
	return preview.String(), nil
}

var _ Tool = rewindFileTool{}
