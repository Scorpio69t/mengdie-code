// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"strings"
	"sync"
)

type boundedOutput struct {
	mu        sync.Mutex
	limit     int
	head      []byte
	tail      []byte
	total     int64
	truncated bool
}

func sanitizeShellOutput(output string) string {
	output = strings.ReplaceAll(output, "\r\n", "\n")
	var sanitized strings.Builder
	for _, char := range output {
		switch {
		case char == '\n' || char == '\t':
			sanitized.WriteRune(char)
		case char < 0x20 || char == 0x7f:
			fmt.Fprintf(&sanitized, "\\x%02x", char)
		case char >= 0x80 && char <= 0x9f:
			fmt.Fprintf(&sanitized, "\\u%04x", char)
		default:
			sanitized.WriteRune(char)
		}
	}
	return sanitized.String()
}

func newBoundedOutput(limit int) *boundedOutput {
	return &boundedOutput{limit: limit}
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	written := len(data)
	output.total += int64(written)
	if output.limit <= 0 {
		output.truncated = output.truncated || written > 0
		return written, nil
	}
	headLimit := output.limit / 2
	if len(output.head) < headLimit {
		count := min(headLimit-len(output.head), len(data))
		output.head = append(output.head, data[:count]...)
		data = data[count:]
	}
	tailLimit := output.limit - headLimit
	if len(data) >= tailLimit {
		output.tail = append(output.tail[:0], data[len(data)-tailLimit:]...)
	} else if len(data) > 0 {
		overflow := len(output.tail) + len(data) - tailLimit
		if overflow > 0 {
			copy(output.tail, output.tail[overflow:])
			output.tail = output.tail[:len(output.tail)-overflow]
		}
		output.tail = append(output.tail, data...)
	}
	output.truncated = output.total > int64(output.limit)
	return written, nil
}

func (output *boundedOutput) snapshot() (string, int64, bool) {
	output.mu.Lock()
	defer output.mu.Unlock()
	head := strings.ToValidUTF8(string(output.head), "�")
	tail := strings.ToValidUTF8(string(output.tail), "�")
	if !output.truncated {
		return head + tail, output.total, false
	}
	omitted := output.total - int64(len(output.head)+len(output.tail))
	return fmt.Sprintf("%s\n… <truncated: %d bytes omitted> …\n%s", head, omitted, tail), output.total, true
}
