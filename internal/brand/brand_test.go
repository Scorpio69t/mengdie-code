// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package brand

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteWelcomeIncludesIdentityAndRuntimeFacts(t *testing.T) {
	info := Info{
		Version:   "1.2.3",
		Commit:    "abc1234",
		BuildDate: "2026-07-30",
		GoVersion: "go1.26.0",
		Platform:  "darwin/arm64",
		WorkDir:   "/workspace/mengdie",
		Model:     "未配置",
		Security:  "工具执行尚未启用",
	}

	var output bytes.Buffer
	if err := WriteWelcome(&output, info); err != nil {
		t.Fatalf("WriteWelcome() error = %v", err)
	}

	for _, want := range append(strings.Split(Mark, "\n"),
		"MengDie Code / 梦蝶 Code",
		"不是记得更多，而是记得更对。",
		info.Version,
		info.Commit,
		info.GoVersion,
		info.Platform,
		info.WorkDir,
		info.Model,
		info.Security,
	) {
		if !strings.Contains(output.String(), want) {
			t.Errorf("WriteWelcome() output does not contain %q", want)
		}
	}
}

func TestTerminalLogoRowsAreRectangularAndDefensive(t *testing.T) {
	for _, test := range []struct {
		name    string
		compact bool
		width   int
		height  int
	}{
		{name: "full", width: 22, height: 14},
		{name: "compact", compact: true, width: 17, height: 12},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows := TerminalLogoRows(test.compact)
			if len(rows) != test.height {
				t.Fatalf("row count=%d, want %d", len(rows), test.height)
			}
			for index, row := range rows {
				if len(row) != test.width {
					t.Fatalf("row %d width=%d, want %d", index, len(row), test.width)
				}
				if strings.Trim(row, " #@") != "" {
					t.Fatalf("row %d contains unsupported raster symbols: %q", index, row)
				}
			}

			rows[0] = "mutated"
			if TerminalLogoRows(test.compact)[0] == "mutated" {
				t.Fatal("TerminalLogoRows returned mutable shared storage")
			}
		})
	}
}
