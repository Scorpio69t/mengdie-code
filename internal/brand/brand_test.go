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
