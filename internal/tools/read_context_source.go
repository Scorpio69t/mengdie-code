// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

const (
	maxContextSourceMessages  = 4
	contextSourcePayloadBytes = 6 << 10
	maxContextSourceOutput    = 16 << 10
	// ReadContextSourceToolName lets the Runtime advertise this tool only
	// after a rolling summary exists.
	ReadContextSourceToolName = "read_context_source"
)

const readContextSourceSchema = `{
  "type": "object",
  "properties": {
    "offset": {"type": "integer", "minimum": 0, "description": "摘要来源内的相对消息偏移，0 表示第一条"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 4, "description": "最多返回的消息数，缺省为 4"},
    "byte_offset": {"type": "integer", "minimum": 0, "description": "超大消息的 JSON 字节续读位置；使用时 limit 必须为 1"}
  },
  "additionalProperties": false
}`

// ContextSourceDescriptor identifies the only private source interval a
// read_context_source tool instance may expose. Session identity is
// deliberately absent from model-visible arguments.
type ContextSourceDescriptor struct {
	SummarySHA256 string
	SourceStart   uint64
	SourceEnd     uint64
}

// ContextSourceMessage is a recovery-safe original context boundary. Write,
// execute and network tool outputs have already been sanitized before they can
// reach this reader.
type ContextSourceMessage struct {
	Ordinal      uint64
	Role         provider.Role
	Completeness string
	SHA256       string
	Message      provider.Message
}

// ContextSourceReader is supplied by the application boundary and is scoped to
// one current Session. Describe is used during Prepare; Load must revalidate the
// expected descriptor during Execute.
type ContextSourceReader interface {
	Describe(context.Context) (ContextSourceDescriptor, error)
	Load(context.Context, ContextSourceDescriptor) ([]ContextSourceMessage, error)
}

type readContextSourceArgs struct {
	Offset     int `json:"offset"`
	Limit      int `json:"limit,omitempty"`
	ByteOffset int `json:"byte_offset,omitempty"`
}

func (a *readContextSourceArgs) normalize() error {
	if a.Offset < 0 || a.ByteOffset < 0 {
		return errors.New("read_context_source: offsets must not be negative")
	}
	if a.Limit == 0 {
		a.Limit = maxContextSourceMessages
	}
	if a.Limit < 1 || a.Limit > maxContextSourceMessages {
		return fmt.Errorf("read_context_source: limit must be between 1 and %d", maxContextSourceMessages)
	}
	if a.ByteOffset > 0 && a.Limit != 1 {
		return errors.New("read_context_source: byte_offset requires limit=1")
	}
	return nil
}

type preparedContextSourceArgs struct {
	Offset        int    `json:"offset"`
	Limit         int    `json:"limit"`
	ByteOffset    int    `json:"byte_offset"`
	SummarySHA256 string `json:"summary_sha256"`
	SourceStart   uint64 `json:"source_start"`
	SourceEnd     uint64 `json:"source_end"`
}

// NewReadContextSource builds a session-scoped, read-only source backfill tool.
func NewReadContextSource(reader ContextSourceReader) (Tool, error) {
	if reader == nil {
		return nil, errors.New("read_context_source: reader is required")
	}
	return readContextSourceTool{reader: reader}, nil
}

type readContextSourceTool struct{ reader ContextSourceReader }

func (readContextSourceTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        ReadContextSourceToolName,
		Description: "仅在出现滚动摘要后，按相对偏移回查当前会话、当前有效摘要覆盖的恢复安全原始上下文；从 offset=0 开始，并按返回的 next_offset/next_byte_offset 继续",
		InputSchema: json.RawMessage(readContextSourceSchema),
		Effects:     []Effect{EffectRead},
	}
}

func (t readContextSourceTool) Prepare(ctx context.Context, raw json.RawMessage, env PrepareEnv) (*PreparedCall, error) {
	var args readContextSourceArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if err := args.normalize(); err != nil {
		return nil, err
	}
	descriptor, err := t.reader.Describe(ctx)
	if err != nil {
		return nil, fmt.Errorf("read_context_source: describe current summary: %w", err)
	}
	count, err := descriptor.messageCount()
	if err != nil {
		return nil, err
	}
	if args.Offset >= count {
		return nil, fmt.Errorf("read_context_source: offset %d outside source length %d", args.Offset, count)
	}
	canonical, err := json.Marshal(preparedContextSourceArgs{
		Offset: args.Offset, Limit: args.Limit, ByteOffset: args.ByteOffset,
		SummarySHA256: descriptor.SummarySHA256,
		SourceStart:   descriptor.SourceStart, SourceEnd: descriptor.SourceEnd,
	})
	if err != nil {
		return nil, fmt.Errorf("read_context_source: encode prepared arguments: %w", err)
	}
	shortHash := strings.TrimPrefix(descriptor.SummarySHA256, "sha256:")
	if len(shortHash) > 12 {
		shortHash = shortHash[:12]
	}
	return PrepareCall(env.CallID, ReadContextSourceToolName, canonical,
		[]Effect{EffectRead}, nil,
		Preview{
			Kind:  PreviewRead,
			Title: "回查当前滚动摘要来源",
			Body:  fmt.Sprintf("摘要 %s，来源 ordinal %d-%d，相对偏移 %d", shortHash, descriptor.SourceStart, descriptor.SourceEnd, args.Offset),
		}, nil,
	)
}

func (t readContextSourceTool) Execute(ctx context.Context, call *PreparedCall, cap Capability, env ExecEnv) (*ToolResult, error) {
	if err := CheckCapability(ctx, call, cap, env); err != nil {
		return nil, err
	}
	var args preparedContextSourceArgs
	if err := decodeArgs(call.CanonicalArg, &args); err != nil {
		return nil, err
	}
	descriptor := ContextSourceDescriptor{
		SummarySHA256: args.SummarySHA256,
		SourceStart:   args.SourceStart,
		SourceEnd:     args.SourceEnd,
	}
	count, err := descriptor.messageCount()
	if err != nil {
		return nil, err
	}
	if args.Offset < 0 || args.Offset >= count || args.Limit < 1 || args.Limit > maxContextSourceMessages || args.ByteOffset < 0 {
		return nil, errors.New("read_context_source: prepared arguments are invalid")
	}
	if args.ByteOffset > 0 && args.Limit != 1 {
		return nil, errors.New("read_context_source: prepared byte_offset requires limit=1")
	}
	messages, err := t.reader.Load(ctx, descriptor)
	if err != nil {
		return nil, fmt.Errorf("read_context_source: load verified source: %w", err)
	}
	if len(messages) != count {
		return nil, errors.New("read_context_source: verified source length changed")
	}
	page, err := buildContextSourcePage(descriptor, messages, args)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		return nil, fmt.Errorf("read_context_source: encode result: %w", err)
	}
	if len(encoded) > maxContextSourceOutput {
		return nil, fmt.Errorf("read_context_source: result exceeds %d bytes", maxContextSourceOutput)
	}
	metadata := map[string]string{
		"summary_sha256": descriptor.SummarySHA256,
		"source_start":   fmt.Sprintf("%d", descriptor.SourceStart),
		"source_end":     fmt.Sprintf("%d", descriptor.SourceEnd),
		"entries":        fmt.Sprintf("%d", len(page.Entries)),
	}
	return &ToolResult{Output: string(encoded), Truncated: page.HasMore, Metadata: metadata}, nil
}

func (d ContextSourceDescriptor) messageCount() (int, error) {
	if strings.TrimSpace(d.SummarySHA256) == "" || d.SourceStart == 0 || d.SourceEnd < d.SourceStart {
		return 0, errors.New("read_context_source: invalid current summary descriptor")
	}
	count := d.SourceEnd - d.SourceStart + 1
	if count > uint64(^uint(0)>>1) {
		return 0, errors.New("read_context_source: summary source is too large")
	}
	return int(count), nil
}

type contextSourcePage struct {
	SummarySHA256 string               `json:"summary_sha256"`
	SourceStart   uint64               `json:"source_start"`
	SourceEnd     uint64               `json:"source_end"`
	Entries       []contextSourceEntry `json:"entries"`
	HasMore       bool                 `json:"has_more"`
	NextOffset    int                  `json:"next_offset,omitempty"`
	NextByte      int                  `json:"next_byte_offset,omitempty"`
}

type contextSourceEntry struct {
	Offset       int           `json:"offset"`
	Ordinal      uint64        `json:"ordinal"`
	Role         provider.Role `json:"role"`
	Completeness string        `json:"completeness"`
	SHA256       string        `json:"sha256"`
	MessageJSON  string        `json:"message_json"`
	ByteStart    int           `json:"byte_start"`
	ByteEnd      int           `json:"byte_end"`
	TotalBytes   int           `json:"total_bytes"`
	Complete     bool          `json:"complete"`
}

func buildContextSourcePage(
	descriptor ContextSourceDescriptor,
	messages []ContextSourceMessage,
	args preparedContextSourceArgs,
) (contextSourcePage, error) {
	page := contextSourcePage{
		SummarySHA256: descriptor.SummarySHA256,
		SourceStart:   descriptor.SourceStart,
		SourceEnd:     descriptor.SourceEnd,
		Entries:       make([]contextSourceEntry, 0, args.Limit),
	}
	remaining := contextSourcePayloadBytes
	for offset := args.Offset; offset < len(messages) && len(page.Entries) < args.Limit && remaining > 0; offset++ {
		message := messages[offset]
		wantOrdinal := descriptor.SourceStart + uint64(offset)
		if message.Ordinal != wantOrdinal || strings.TrimSpace(message.SHA256) == "" || strings.TrimSpace(message.Completeness) == "" {
			return contextSourcePage{}, fmt.Errorf("read_context_source: source identity mismatch at offset %d", offset)
		}
		encoded, err := json.Marshal(message.Message)
		if err != nil {
			return contextSourcePage{}, fmt.Errorf("read_context_source: encode message %d: %w", message.Ordinal, err)
		}
		start := 0
		if offset == args.Offset {
			start = args.ByteOffset
		}
		if start >= len(encoded) || !utf8.RuneStart(encoded[start]) {
			return contextSourcePage{}, fmt.Errorf("read_context_source: byte_offset %d is not a valid boundary for offset %d", start, offset)
		}
		end := len(encoded)
		if end-start > remaining {
			end = start + remaining
			for end > start && end < len(encoded) && !utf8.RuneStart(encoded[end]) {
				end--
			}
		}
		if end == start && start < len(encoded) {
			return contextSourcePage{}, errors.New("read_context_source: source page budget cannot fit one rune")
		}
		entry := contextSourceEntry{
			Offset: offset, Ordinal: message.Ordinal, Role: message.Role,
			Completeness: message.Completeness, SHA256: message.SHA256,
			MessageJSON: string(encoded[start:end]), ByteStart: start, ByteEnd: end,
			TotalBytes: len(encoded), Complete: start == 0 && end == len(encoded),
		}
		page.Entries = append(page.Entries, entry)
		remaining -= end - start
		if end < len(encoded) {
			page.HasMore = true
			page.NextOffset = offset
			page.NextByte = end
			break
		}
		if offset+1 < len(messages) {
			page.HasMore = true
			page.NextOffset = offset + 1
			page.NextByte = 0
		}
	}
	if len(page.Entries) == 0 {
		return contextSourcePage{}, errors.New("read_context_source: no source content returned")
	}
	return page, nil
}
