// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

const writeFileSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "目标文件路径，相对项目根或绝对路径"},
    "content": {"type": "string", "description": "完整 UTF-8 文件内容"},
    "overwrite": {"type": "boolean", "description": "必须显式为 true 才能覆盖已有文件"}
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`

type writeFileArgs struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Overwrite bool   `json:"overwrite"`
}

func (args *writeFileArgs) validate() error {
	if strings.TrimSpace(args.Path) == "" {
		return errors.New("write_file: path is required")
	}
	if len(args.Content) > maxWriteContentBytes {
		return fmt.Errorf("write_file: content exceeds %d-byte limit", maxWriteContentBytes)
	}
	return validateText([]byte(args.Content), "write_file", "content")
}

// NewWriteFile builds a complete-file create/overwrite tool.
func NewWriteFile() Tool { return writeFileTool{} }

type writeFileTool struct{}

func (writeFileTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "write_file",
		Description: "创建 UTF-8 文本文件；覆盖已有文件必须显式声明并批准完整 diff",
		InputSchema: json.RawMessage(writeFileSchema),
		Effects:     []Effect{EffectWrite},
	}
}

func (writeFileTool) Prepare(ctx context.Context, raw json.RawMessage, env PrepareEnv) (*PreparedCall, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var args writeFileArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if err := args.validate(); err != nil {
		return nil, err
	}
	resolved, err := env.Guard.Resolve(args.Path, platform.AccessWrite)
	if err != nil {
		return nil, err
	}
	info, statErr := os.Stat(resolved.Path)
	exists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("write_file: stat: %w", statErr)
	}
	if exists && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("write_file: %q is not a regular file", args.Path)
	}
	if exists != args.Overwrite {
		if exists {
			return nil, errors.New("write_file: target exists; set overwrite=true to replace it")
		}
		return nil, errors.New("write_file: overwrite=true requires an existing target")
	}

	var before []byte
	var preconditions []Precondition
	operation := "创建"
	if exists {
		before, _, err = readEditableFile(resolved.Path, "write_file")
		if err != nil {
			return nil, err
		}
		preconditions = []Precondition{{Kind: PreconditionFileSHA256, Path: resolved.Path, SHA256: bytesSHA256(before)}}
		if bytesSHA256(before) == bytesSHA256([]byte(args.Content)) {
			return nil, errors.New("write_file: replacement content is identical to the existing file")
		}
		operation = "覆盖"
	} else {
		preconditions = []Precondition{{Kind: PreconditionPathAbsent, Path: resolved.Path}}
	}
	preview, err := fullFileDiff(resolved.Path, before, []byte(args.Content), !exists)
	if err != nil {
		return nil, err
	}
	missingParents := countMissingParents(filepath.Dir(resolved.Path))
	title := fmt.Sprintf("%s %s", operation, resolved.Path)
	if missingParents > 0 {
		title += fmt.Sprintf("（同时创建 %d 层父目录）", missingParents)
	}
	return PrepareCall(env.CallID, "write_file", raw,
		[]Effect{EffectWrite},
		[]PathResource{{Path: resolved.Path}},
		Preview{Kind: PreviewDiff, Title: title, Body: preview},
		preconditions,
	)
}

func (writeFileTool) Execute(ctx context.Context, call *PreparedCall, cap Capability, env ExecEnv) (*ToolResult, error) {
	if err := CheckCapability(ctx, call, cap, env); err != nil {
		return nil, err
	}
	var args writeFileArgs
	if err := decodeArgs(call.CanonicalArg, &args); err != nil {
		return nil, err
	}
	if err := args.validate(); err != nil {
		return nil, err
	}
	resolved, err := env.Guard.Resolve(args.Path, platform.AccessWrite)
	if err != nil {
		return nil, err
	}
	if err := ensureSamePreparedPath(call, resolved.Path); err != nil {
		return nil, err
	}
	if err := CheckPreconditions(call.Preconditions); err != nil {
		return nil, err
	}
	mode := os.FileMode(0o644)
	if args.Overwrite {
		info, err := os.Stat(resolved.Path)
		if err != nil {
			return nil, &PreconditionError{Path: resolved.Path, Reason: err.Error()}
		}
		if !info.Mode().IsRegular() {
			return nil, &PreconditionError{Path: resolved.Path, Reason: "target is no longer a regular file"}
		}
		mode = info.Mode()
	}
	content := []byte(args.Content)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if env.MutationJournal == nil {
		return nil, ErrMutationJournalMissing
	}
	var receipt MutationReceipt
	prepareJournal := func() error {
		preExists := args.Overwrite
		var preMode os.FileMode
		var preContent []byte
		var journalErr error
		if preExists {
			preMode = mode
			preContent, _, journalErr = readEditableFile(resolved.Path, "write_file")
			if journalErr != nil {
				return journalErr
			}
		}
		receipt, journalErr = env.MutationJournal.Prepare(ctx, MutationIntent{
			ToolCallID: call.ID, ToolName: call.ToolName, CallDigest: call.Digest,
			Path: resolved.Path, PreExists: preExists, PreSHA256: preconditionsHash(call.Preconditions), PreMode: preMode,
			PreContent: preContent,
			PostExists: true, PostSHA256: bytesSHA256(content), PostMode: mode,
		})
		if journalErr == nil {
			journalErr = ctx.Err()
		}
		return journalErr
	}
	if err := atomicWriteFile(env.Guard.Root(), resolved.Path, content, mode, args.Overwrite, call.Preconditions, prepareJournal); err != nil {
		return nil, fmt.Errorf("write_file: %w", err)
	}
	journalContext := context.WithoutCancel(ctx)
	if err := env.MutationJournal.MarkApplied(journalContext, receipt); err != nil {
		return nil, fmt.Errorf("write_file: record applied write: %w", err)
	}
	if err := env.MutationJournal.VerifyPost(journalContext, receipt); err != nil {
		return nil, fmt.Errorf("write_file: verify applied write: %w", err)
	}
	operation := "created"
	if args.Overwrite {
		operation = "overwritten"
	}
	return &ToolResult{
		Output: fmt.Sprintf("已写入 %s（%d 字节）", resolved.Path, len(content)),
		Metadata: map[string]string{
			"path":       resolved.Path,
			"operation":  operation,
			"sha256":     bytesSHA256(content),
			"size_bytes": fmt.Sprintf("%d", len(content)),
		},
	}, nil
}

func countMissingParents(dir string) int {
	count := 0
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Stat(current); err == nil {
			return count
		} else if !errors.Is(err, os.ErrNotExist) {
			return count
		}
		count++
		if parent := filepath.Dir(current); parent == current {
			return count
		}
	}
}
