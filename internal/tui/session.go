// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package tui contains replaceable, read-only terminal views over public
// application facts. It never opens the EventStore or invokes tools.
package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// SessionModel renders one public SessionView. Later slices may feed it replay
// and subscription messages, but this model deliberately owns no persistence.
type SessionModel struct {
	view  session.SessionView
	width int
	color bool
}

func NewSessionModel(view session.SessionView, width int, color bool) SessionModel {
	return SessionModel{view: view, width: width, color: color}
}

func (m SessionModel) Init() tea.Cmd { return nil }

func (m SessionModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch value := message.(type) {
	case tea.WindowSizeMsg:
		m.width = value.Width
	case tea.KeyPressMsg:
		if value.String() == "q" || value.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m SessionModel) View() tea.View {
	result := tea.NewView(RenderSession(m.view, m.width, m.color))
	result.AltScreen = true
	result.WindowTitle = "MengDie Code · 会话"
	return result
}

// RenderSession is deterministic so tests can cover Chinese, narrow-terminal,
// and no-color behavior without starting a terminal program.
func RenderSession(view session.SessionView, width int, color bool) string {
	if width > 0 && width < 42 {
		return fmt.Sprintf("梦蝶 Code\n会话 %s\n状态 %s\n终端过窄，请扩大到至少 42 列。\n按 q 退出", clip(view.ID, 24), view.Status)
	}
	heading := "梦蝶 Code · 会话"
	if color {
		heading = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Render(heading)
	}
	lines := []string{heading, fmt.Sprintf("%s · %s · %d 条事实", clip(view.ID, 48), view.Status, view.LastSeq), ""}
	lines = append(lines, "时间线：")
	for _, message := range view.Messages {
		lines = append(lines, "- "+clip(strings.TrimSpace(message.Text), 96))
	}
	if len(view.Messages) == 0 {
		lines = append(lines, "- 暂无完成消息")
	}
	lines = append(lines, "", "工具与审批：")
	for _, tool := range view.Tools {
		lines = append(lines, fmt.Sprintf("- %s：%s", clip(tool.Tool, 36), tool.Phase))
	}
	for _, approval := range view.Approvals {
		if approval.Decision == "" {
			lines = append(lines, fmt.Sprintf("- 待审批调用 %s", clip(approval.CallID, 32)))
		}
	}
	lines = append(lines, "", "待办：")
	for _, todo := range view.Todos {
		lines = append(lines, fmt.Sprintf("- [%s] %s", todo.Status, clip(todo.Content, 72)))
	}
	if len(view.Todos) == 0 {
		lines = append(lines, "- 暂无待办")
	}
	lines = append(lines, "", "受控本地执行 · 公开事实视图 · q 退出")
	return strings.Join(lines, "\n")
}

func clip(value string, limit int) string {
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	return string([]rune(value)[:limit-1]) + "…"
}
