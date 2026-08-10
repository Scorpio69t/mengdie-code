// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package tui contains replaceable terminal views over application-owned task
// submission and public facts. It never opens the EventStore or invokes tools.
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// SessionModel renders one public SessionView and deliberately owns no
// persistence.
type SessionModel struct {
	view         session.SessionView
	width        int
	color        bool
	factSource   SessionFactSource
	subscription FactSubscription
	feedError    error
}

// SessionFactSource is owned by the TUI consumer. Its implementation remains
// behind the application/session service boundary and may use EventStore plus
// a replaceable same-process notification bus.
type SessionFactSource interface {
	ReplayPublicFacts(context.Context, string, uint64, int) (session.PublicFactPage, error)
	SubscribePublicFacts(string, uint64) (FactSubscription, error)
}

type FactSubscription interface {
	Notifications() <-chan session.PublicFactNotification
	Close()
}

const factReplayPageSize = 256

func NewSessionModel(view session.SessionView, width int, color bool) SessionModel {
	return SessionModel{view: view, width: width, color: color}
}

// NewSubscribedSessionModel adds committed-fact replay and notifications to
// the read-only view. It still owns no persistence or execution capability.
func NewSubscribedSessionModel(view session.SessionView, width int, color bool, source SessionFactSource) SessionModel {
	return SessionModel{view: view, width: width, color: color, factSource: source}
}

func (m SessionModel) Init() tea.Cmd {
	if m.factSource == nil {
		return nil
	}
	return startFactFeed(m.factSource, m.view.ID, m.view.LastSeq)
}

func (m SessionModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch value := message.(type) {
	case tea.WindowSizeMsg:
		m.width = value.Width
	case tea.KeyPressMsg:
		if value.String() == "q" || value.String() == "ctrl+c" {
			m.closeSubscription()
			return m, tea.Quit
		}
	case factFeedStartedMsg:
		m.subscription = value.subscription
		return m.applyReplay(value.page)
	case factReplayMsg:
		return m.applyReplay(value.page)
	case factNotificationMsg:
		if !value.open {
			m.subscription = nil
			return m, nil
		}
		if value.notification.Fact.SessionSeq <= m.view.LastSeq {
			return m, waitForFact(m.subscription)
		}
		if value.notification.Gap || value.notification.Fact.SessionSeq != m.view.LastSeq+1 {
			return m, replayFacts(m.factSource, m.view.ID, m.view.LastSeq)
		}
		view, err := session.ReducePublicFacts(m.view, []session.PublicFact{value.notification.Fact})
		if err != nil {
			m.feedError = err
			m.closeSubscription()
			return m, nil
		}
		m.view, m.feedError = view, nil
		return m, waitForFact(m.subscription)
	case factFeedErrorMsg:
		m.feedError = value.err
		m.closeSubscription()
	}
	return m, nil
}

func (m SessionModel) applyReplay(page session.PublicFactPage) (tea.Model, tea.Cmd) {
	facts := page.Facts[:0]
	for _, fact := range page.Facts {
		if fact.SessionSeq > m.view.LastSeq {
			facts = append(facts, fact)
		}
	}
	view, err := session.ReducePublicFacts(m.view, facts)
	if err != nil {
		m.feedError = err
		m.closeSubscription()
		return m, nil
	}
	if page.ThroughSeq > view.LastSeq {
		view.LastSeq = page.ThroughSeq
	}
	m.view, m.feedError = view, nil
	if page.More {
		return m, replayFacts(m.factSource, m.view.ID, m.view.LastSeq)
	}
	return m, waitForFact(m.subscription)
}

func (m *SessionModel) closeSubscription() {
	if m.subscription != nil {
		m.subscription.Close()
		m.subscription = nil
	}
}

type factFeedStartedMsg struct {
	subscription FactSubscription
	page         session.PublicFactPage
}

type factReplayMsg struct{ page session.PublicFactPage }
type factFeedErrorMsg struct{ err error }
type factNotificationMsg struct {
	notification session.PublicFactNotification
	open         bool
}

func startFactFeed(source SessionFactSource, sessionID string, afterSeq uint64) tea.Cmd {
	return func() tea.Msg {
		subscription, err := source.SubscribePublicFacts(sessionID, afterSeq)
		if err != nil {
			return factFeedErrorMsg{err: err}
		}
		page, err := source.ReplayPublicFacts(context.Background(), sessionID, afterSeq, factReplayPageSize)
		if err != nil {
			subscription.Close()
			return factFeedErrorMsg{err: err}
		}
		return factFeedStartedMsg{subscription: subscription, page: page}
	}
}

func replayFacts(source SessionFactSource, sessionID string, afterSeq uint64) tea.Cmd {
	return func() tea.Msg {
		page, err := source.ReplayPublicFacts(context.Background(), sessionID, afterSeq, factReplayPageSize)
		if err != nil {
			return factFeedErrorMsg{err: err}
		}
		return factReplayMsg{page: page}
	}
}

func waitForFact(subscription FactSubscription) tea.Cmd {
	if subscription == nil {
		return nil
	}
	return func() tea.Msg {
		notification, open := <-subscription.Notifications()
		return factNotificationMsg{notification: notification, open: open}
	}
}

func (m SessionModel) View() tea.View {
	content := RenderSession(m.view, m.width, m.color)
	if m.feedError != nil {
		content += "\n\n实时事实流已停止，可退出后重新打开：" + clip(m.feedError.Error(), 72)
	}
	result := tea.NewView(content)
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
	lines = append(lines, renderSessionTimeline(view, width), "", "受控本地执行 · 公开事实视图 · q 退出")
	return strings.Join(lines, "\n")
}

func renderSessionTimeline(view session.SessionView, width int) string {
	messageLimit := 96
	if width > 0 {
		messageLimit = max(24, width-6)
	}
	lines := []string{fmt.Sprintf("会话 %s · %s · %d 条已提交事实", clip(view.ID, 48), view.Status, view.LastSeq), "", "时间线："}
	for _, message := range view.Messages {
		lines = append(lines, "- "+clip(strings.TrimSpace(message.Text), messageLimit))
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
	return strings.Join(lines, "\n")
}

func clip(value string, limit int) string {
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	return string([]rune(value)[:limit-1]) + "…"
}
