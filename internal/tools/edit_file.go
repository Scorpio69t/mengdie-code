// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

const editFileSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "待修改文件路径，相对项目根或绝对路径"},
    "old_text": {"type": "string", "description": "必须精确匹配的原文本"},
    "new_text": {"type": "string", "description": "替换后的文本"},
    "expected_replacements": {"type": "integer", "minimum": 1, "maximum": 1000, "description": "期望精确替换次数，默认 1"}
  },
  "required": ["path", "old_text", "new_text"],
  "additionalProperties": false
}`

type editFileArgs struct {
	Path                 string `json:"path"`
	OldText              string `json:"old_text"`
	NewText              string `json:"new_text"`
	ExpectedReplacements int    `json:"expected_replacements"`
}

func (args *editFileArgs) validate() error {
	if strings.TrimSpace(args.Path) == "" {
		return errors.New("edit_file: path is required")
	}
	if args.OldText == "" {
		return errors.New("edit_file: old_text must not be empty")
	}
	if args.OldText == args.NewText {
		return errors.New("edit_file: old_text and new_text are identical")
	}
	if args.ExpectedReplacements == 0 {
		args.ExpectedReplacements = 1
	}
	if args.ExpectedReplacements < 1 || args.ExpectedReplacements > 1000 {
		return errors.New("edit_file: expected_replacements must be between 1 and 1000")
	}
	if len(args.OldText) > maxEditTextBytes || len(args.NewText) > maxEditTextBytes {
		return fmt.Errorf("edit_file: old_text and new_text must each be at most %d bytes", maxEditTextBytes)
	}
	if err := validateText([]byte(args.OldText), "edit_file", "old_text"); err != nil {
		return err
	}
	return validateText([]byte(args.NewText), "edit_file", "new_text")
}

// NewEditFile builds a deterministic exact-edit tool.
func NewEditFile() Tool { return editFileTool{} }

type editFileTool struct{}

func (editFileTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "edit_file",
		Description: "按精确文本和期望次数修改已有 UTF-8 文件；批准前展示完整替换预览",
		InputSchema: json.RawMessage(editFileSchema),
		Effects:     []Effect{EffectWrite},
	}
}

func (editFileTool) Prepare(ctx context.Context, raw json.RawMessage, env PrepareEnv) (*PreparedCall, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var args editFileArgs
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
	content, _, err := readEditableFile(resolved.Path, "edit_file")
	if err != nil {
		return nil, err
	}
	replacements := strings.Count(string(content), args.OldText)
	if replacements != args.ExpectedReplacements {
		return nil, fmt.Errorf("edit_file: old_text matched %d times, expected %d", replacements, args.ExpectedReplacements)
	}
	updated := []byte(strings.Replace(string(content), args.OldText, args.NewText, args.ExpectedReplacements))
	if len(updated) > maxEditableFileBytes {
		return nil, fmt.Errorf("edit_file: updated file exceeds %d-byte edit limit", maxEditableFileBytes)
	}
	preview, err := exactEditDiff(resolved.Path, args.OldText, args.NewText, replacements)
	if err != nil {
		return nil, err
	}
	return PrepareCall(env.CallID, "edit_file", raw,
		[]Effect{EffectWrite},
		[]PathResource{{Path: resolved.Path}},
		Preview{Kind: PreviewDiff, Title: fmt.Sprintf("修改 %s（%d 处）", resolved.Path, replacements), Body: preview},
		[]Precondition{{Kind: PreconditionFileSHA256, Path: resolved.Path, SHA256: bytesSHA256(content)}},
	)
}

func (editFileTool) Execute(ctx context.Context, call *PreparedCall, cap Capability, env ExecEnv) (*ToolResult, error) {
	if err := CheckCapability(ctx, call, cap, env); err != nil {
		return nil, err
	}
	var args editFileArgs
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
	content, mode, err := readEditableFile(resolved.Path, "edit_file")
	if err != nil {
		return nil, err
	}
	replacements := strings.Count(string(content), args.OldText)
	if replacements != args.ExpectedReplacements {
		return nil, &PreconditionError{Path: resolved.Path, Reason: "exact match count changed after approval"}
	}
	updated := []byte(strings.Replace(string(content), args.OldText, args.NewText, args.ExpectedReplacements))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if env.MutationJournal == nil {
		return nil, ErrMutationJournalMissing
	}
	var receipt MutationReceipt
	prepareJournal := func() error {
		var journalErr error
		receipt, journalErr = env.MutationJournal.Prepare(ctx, MutationIntent{
			ToolCallID: call.ID, ToolName: call.ToolName, CallDigest: call.Digest,
			Path: resolved.Path, PreExists: true, PreSHA256: preconditionsHash(call.Preconditions), PreMode: mode,
			PreContent: append([]byte(nil), content...),
			PostExists: true, PostSHA256: bytesSHA256(updated), PostMode: mode,
		})
		if journalErr == nil {
			journalErr = ctx.Err()
		}
		return journalErr
	}
	if err := atomicWriteFile(env.Guard.Root(), resolved.Path, updated, mode, true, call.Preconditions, prepareJournal); err != nil {
		return nil, fmt.Errorf("edit_file: %w", err)
	}
	journalContext := context.WithoutCancel(ctx)
	if err := env.MutationJournal.MarkApplied(journalContext, receipt); err != nil {
		return nil, fmt.Errorf("edit_file: record applied write: %w", err)
	}
	if err := env.MutationJournal.VerifyPost(journalContext, receipt); err != nil {
		return nil, fmt.Errorf("edit_file: verify applied write: %w", err)
	}
	return &ToolResult{
		Output: fmt.Sprintf("已修改 %s（%d 处精确替换）", resolved.Path, replacements),
		Metadata: map[string]string{
			"path":          resolved.Path,
			"sha256_before": preconditionsHash(call.Preconditions),
			"sha256_after":  bytesSHA256(updated),
			"replacements":  fmt.Sprintf("%d", replacements),
		},
	}, nil
}
