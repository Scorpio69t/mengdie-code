// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

const (
	defaultApprovalInputBytes = 4 << 10
	defaultApprovalAttempts   = 3
)

// TextBroker provides the M1 terminal approval interaction. A mutex prevents
// prompts and responses from different calls interleaving.
type TextBroker struct {
	mu       sync.Mutex
	reader   *bufio.Reader
	writer   io.Writer
	maxBytes int
	attempts int
}

func NewTextBroker(reader io.Reader, writer io.Writer) (*TextBroker, error) {
	if reader == nil || writer == nil {
		return nil, errors.New("policy: approval input and output are required")
	}
	return &TextBroker{
		reader: bufio.NewReader(reader), writer: writer,
		maxBytes: defaultApprovalInputBytes, attempts: defaultApprovalAttempts,
	}, nil
}

func (b *TextBroker) Decide(ctx context.Context, request ApprovalRequest) (ApprovalResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for attempt := 0; attempt < b.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return ApprovalResponse{}, err
		}
		if _, err := fmt.Fprintf(b.writer, "%s [y]允许 / [n]拒绝 / [e]编辑后重试（风险：%s）: ", request.Prompt, request.Risk); err != nil {
			return ApprovalResponse{}, fmt.Errorf("write approval prompt: %w", err)
		}
		line, err := b.readLine(ctx)
		if err != nil {
			return ApprovalResponse{}, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes", "是", "允许", "批准":
			return ApprovalResponse{Choice: ApprovalApprove}, nil
		case "n", "no", "否", "拒绝":
			return ApprovalResponse{Choice: ApprovalReject}, nil
		case "e", "edit", "编辑":
			return ApprovalResponse{Choice: ApprovalEdit}, nil
		default:
			if _, err := io.WriteString(b.writer, "请输入 y、n 或 e。\n"); err != nil {
				return ApprovalResponse{}, fmt.Errorf("write approval help: %w", err)
			}
		}
	}
	return ApprovalResponse{}, errors.New("policy: too many invalid approval responses")
}

func (b *TextBroker) readLine(ctx context.Context) (string, error) {
	var builder strings.Builder
	tooLong := false
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		part, more, err := b.reader.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && builder.Len() > 0 {
				break
			}
			return "", fmt.Errorf("read approval response: %w", err)
		}
		if builder.Len()+len(part) > b.maxBytes {
			tooLong = true
		} else if !tooLong {
			builder.Write(part)
		}
		if !more {
			break
		}
	}
	if tooLong {
		return "", errors.New("policy: approval response exceeds size limit")
	}
	return builder.String(), nil
}
