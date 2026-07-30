// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
)

var errSSEEventTooLarge = errors.New("SSE event exceeds configured limit")

func parseSSE(
	ctx context.Context,
	reader io.Reader,
	maxEventBytes int,
	onData func([]byte) error,
) (bool, error) {
	if reader == nil {
		return false, errors.New("SSE reader is required")
	}
	if maxEventBytes <= 0 {
		return false, errors.New("SSE event limit must be positive")
	}
	if onData == nil {
		return false, errors.New("SSE data callback is required")
	}
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var data []byte
	firstLine := true
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		line, ok, err := readLimitedLine(buffered, maxEventBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// WHATWG discards a pending event that was not terminated by a
				// blank line. The caller decides whether the completed stream is
				// still valid based on its finish_reason.
				return false, nil
			}
			return false, err
		}
		if !ok {
			return false, nil
		}
		if firstLine {
			line = bytes.TrimPrefix(line, []byte{0xef, 0xbb, 0xbf})
			firstLine = false
		}
		if len(line) == 0 {
			if len(data) == 0 {
				continue
			}
			payload := append([]byte(nil), data...)
			data = data[:0]
			if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
				return true, nil
			}
			if err := onData(payload); err != nil {
				return false, err
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte{':'})
		if found && len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		if !bytes.Equal(field, []byte("data")) {
			continue
		}
		additional := len(value)
		if len(data) > 0 {
			additional++
		}
		if len(data)+additional > maxEventBytes {
			return false, errSSEEventTooLarge
		}
		if len(data) > 0 {
			data = append(data, '\n')
		}
		data = append(data, value...)
	}
}

func readLimitedLine(reader *bufio.Reader, limit int) ([]byte, bool, error) {
	var line []byte
	for {
		fragment, more, err := reader.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return line, true, nil
			}
			return nil, false, err
		}
		if len(line)+len(fragment) > limit {
			return nil, false, fmt.Errorf("%w: line", errSSEEventTooLarge)
		}
		line = append(line, fragment...)
		if !more {
			return line, true, nil
		}
	}
}
