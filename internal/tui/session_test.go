// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

func TestRenderSessionUsesOnlyPublicFactsAndHandlesNarrowWidth(t *testing.T) {
	view := session.SessionView{ID: "会话-测试", Status: "interrupted", LastSeq: 8,
		Messages:  []session.MessageView{{Text: "修复中文宽字符"}},
		Tools:     []session.ToolView{{Tool: "read_file", Phase: "completed"}},
		Approvals: []session.ApprovalView{{CallID: "call-1"}},
		Todos:     []events.Todo{{Content: "验证 Windows", Status: "in_progress"}},
	}
	output := RenderSession(view, 100, false)
	for _, want := range []string{"修复中文宽字符", "read_file：completed", "待审批调用 call-1", "验证 Windows", "公开事实视图"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %s", want, output)
		}
	}
	if narrow := RenderSession(view, 30, false); !strings.Contains(narrow, "终端过窄") {
		t.Fatalf("narrow output=%q", narrow)
	}
}
