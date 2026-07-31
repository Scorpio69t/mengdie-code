// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import "fmt"

// Output budgets from the M1 context contract (DETAILED_DESIGN.md §9.3).
const (
	// DefaultFileReadBytes bounds a single read_file result.
	DefaultFileReadBytes = 32 << 10
	// DefaultToolOutputBytes bounds any tool result text.
	DefaultToolOutputBytes = 64 << 10
	// MaxMatchLineLength bounds one search result line.
	MaxMatchLineLength = 500
)

// truncateHead keeps the head of s within max bytes, appending a marker
// that states the omitted size. Truncation never splits a UTF-8 sequence.
func truncateHead(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return fmt.Sprintf("%s\n… <truncated: %d bytes omitted>", s[:cut], len(s)-cut), true
}

// truncateHeadTail keeps both ends of s within max bytes, marking the
// omitted middle (DETAILED_DESIGN.md §9.3: tool output 保留开头和结尾).
// Truncation never splits a UTF-8 sequence.
func truncateHeadTail(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	head := max / 2
	for head > 0 && (s[head]&0xC0) == 0x80 {
		head--
	}
	start := len(s) - (max - head)
	for start < len(s) && (s[start]&0xC0) == 0x80 {
		start++
	}
	return fmt.Sprintf("%s\n… <truncated: %d bytes omitted> …\n%s", s[:head], start-head, s[start:]), true
}

// truncateRunes bounds a single display line by rune count.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
