// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateHeadTailKeepsBothEnds(t *testing.T) {
	s := strings.Repeat("h", 100) + strings.Repeat("m", 100000) + strings.Repeat("t", 100)

	out, truncated := truncateHeadTail(s, 1024)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !strings.HasPrefix(out, strings.Repeat("h", 50)) {
		t.Error("head not preserved")
	}
	if !strings.HasSuffix(out, strings.Repeat("t", 100)) {
		t.Error("tail not preserved")
	}
	if !strings.Contains(out, "bytes omitted") {
		t.Error("omission marker missing")
	}
	if !utf8.ValidString(out) {
		t.Error("output is not valid UTF-8")
	}
}

func TestTruncateHeadTailUTF8Safe(t *testing.T) {
	// 3-byte runes straddle both cut points.
	out, truncated := truncateHeadTail(strings.Repeat("梦", 20000), 1000)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !utf8.ValidString(out) {
		t.Fatal("truncation split a UTF-8 sequence")
	}
}

func TestTruncateHeadTailShortInput(t *testing.T) {
	out, truncated := truncateHeadTail("short", 1024)
	if truncated || out != "short" {
		t.Fatalf("short input altered: %q truncated=%v", out, truncated)
	}
}
