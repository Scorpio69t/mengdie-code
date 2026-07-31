// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

const readFileSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "文件路径，相对项目根或绝对路径"},
    "start_line": {"type": "integer", "minimum": 1, "description": "起始行（1 起始，含），缺省从第 1 行开始"},
    "end_line": {"type": "integer", "minimum": 1, "description": "结束行（含），缺省读到输出上限"}
  },
  "required": ["path"],
  "additionalProperties": false
}`

type readFileArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func (a *readFileArgs) validate() error {
	if strings.TrimSpace(a.Path) == "" {
		return errors.New("read_file: path is required")
	}
	if a.StartLine < 0 || a.EndLine < 0 {
		return errors.New("read_file: line numbers must be positive")
	}
	if a.StartLine > 0 && a.EndLine > 0 && a.EndLine < a.StartLine {
		return fmt.Errorf("read_file: end_line %d before start_line %d", a.EndLine, a.StartLine)
	}
	return nil
}

// NewReadFile builds the read_file tool: line-numbered, bounded file reads
// with binary detection and a content-hash precondition.
func NewReadFile() Tool { return readFileTool{} }

type readFileTool struct{}

func (readFileTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "read_file",
		Description: "读取项目内文本文件，带行号、行范围、编码与截断信息；二进制文件只返回类型和大小",
		InputSchema: json.RawMessage(readFileSchema),
		Effects:     []Effect{EffectRead},
	}
}

func (readFileTool) Prepare(ctx context.Context, raw json.RawMessage, env PrepareEnv) (*PreparedCall, error) {
	var args readFileArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if err := args.validate(); err != nil {
		return nil, err
	}
	resolved, err := env.Guard.Resolve(args.Path, platform.AccessRead)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved.Path)
	if err != nil {
		return nil, fmt.Errorf("read_file: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("read_file: %q is a directory", args.Path)
	}
	hash, err := FileSHA256(resolved.Path)
	if err != nil {
		return nil, fmt.Errorf("read_file: hash: %w", err)
	}

	title := resolved.Path
	if resolved.Sensitive {
		title += "（敏感文件）"
	}
	return PrepareCall(env.CallID, "read_file", raw,
		[]Effect{EffectRead},
		Preview{Kind: PreviewRead, Title: title, Body: fmt.Sprintf("读取 %d 字节", info.Size())},
		[]Precondition{{Kind: PreconditionFileSHA256, Path: resolved.Path, SHA256: hash}},
	)
}

func (readFileTool) Execute(ctx context.Context, call *PreparedCall, cap Capability, env ExecEnv) (*ToolResult, error) {
	if err := CheckCapability(call, cap); err != nil {
		return nil, err
	}
	var args readFileArgs
	if err := decodeArgs(call.CanonicalArg, &args); err != nil {
		return nil, err
	}
	if err := args.validate(); err != nil {
		return nil, err
	}
	// Re-resolve after approval: the target must still be inside the root.
	resolved, err := env.Guard.Resolve(args.Path, platform.AccessRead)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(resolved.Path)
	if err != nil {
		return nil, fmt.Errorf("read_file: %w", err)
	}
	defer file.Close()
	// Hash the open descriptor rather than re-opening by path, so the
	// precondition check and the content read cannot observe two
	// different files.
	if err := CheckFilePreconditions(call.Preconditions, file); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("read_file: stat: %w", err)
	}

	metadata := map[string]string{
		"path":       resolved.Path,
		"sha256":     preconditionsHash(call.Preconditions),
		"size_bytes": fmt.Sprintf("%d", info.Size()),
	}
	if args.StartLine > 0 {
		metadata["start_line"] = fmt.Sprintf("%d", args.StartLine)
	}
	if args.EndLine > 0 {
		metadata["end_line"] = fmt.Sprintf("%d", args.EndLine)
	}

	// Sized to the sniff window so Peek(8<<10) never hits ErrBufferFull.
	reader := bufio.NewReaderSize(file, 8<<10)
	if sniffBinary(reader) {
		metadata["encoding"] = "binary"
		return &ToolResult{
			Output:   fmt.Sprintf("%s：二进制文件，%d 字节，内容不注入上下文", resolved.Path, info.Size()),
			Metadata: metadata,
		}, nil
	}
	metadata["encoding"] = "utf-8"

	output, truncated, returned, err := collectLines(ctx, reader, args.StartLine, args.EndLine)
	if err != nil {
		return nil, err
	}
	metadata["lines_returned"] = fmt.Sprintf("%d", returned)
	return &ToolResult{Output: output, Truncated: truncated, Metadata: metadata}, nil
}

func preconditionsHash(preconditions []Precondition) string {
	for _, precondition := range preconditions {
		if precondition.Kind == PreconditionFileSHA256 {
			return precondition.SHA256
		}
	}
	return ""
}

// sniffBinary reports whether the head of the file looks binary: a NUL
// byte or invalid UTF-8 in the first 8 KiB. It buffers the reader so no
// bytes are consumed.
func sniffBinary(reader *bufio.Reader) bool {
	head, err := reader.Peek(8 << 10)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return false
	}
	if len(head) == 0 {
		return false
	}
	return strings.ContainsRune(string(head), 0) || !utf8.Valid(head)
}

// collectLines reads the requested 1-based inclusive line range, formats
// lines with numbers and applies the file-read byte budget.
func collectLines(ctx context.Context, reader *bufio.Reader, startLine, endLine int) (string, bool, int, error) {
	if startLine < 1 {
		startLine = 1
	}
	var b strings.Builder
	lineNo := 0
	returned := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", false, 0, err
		}
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			lineNo++
			if lineNo >= startLine && (endLine == 0 || lineNo <= endLine) {
				returned++
				fmt.Fprintf(&b, "%d\t%s", lineNo, truncateRunes(strings.TrimRight(line, "\r\n"), MaxMatchLineLength))
				b.WriteByte('\n')
				if b.Len() > DefaultFileReadBytes*2 {
					// Already far beyond budget; stop reading early.
					break
				}
			}
			if endLine > 0 && lineNo >= endLine {
				break
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", false, 0, fmt.Errorf("read_file: read: %w", err)
		}
	}
	if returned == 0 {
		if endLine > 0 {
			return fmt.Sprintf("（指定行范围 %d-%d 内没有内容）", startLine, endLine), false, 0, nil
		}
		return fmt.Sprintf("（从第 %d 行起没有内容）", startLine), false, 0, nil
	}
	output, truncated := truncateHead(strings.TrimRight(b.String(), "\n"), DefaultFileReadBytes)
	return output, truncated, returned, nil
}
