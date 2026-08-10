// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Scorpio69t/mengdie-code/internal/brand"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

const MaxInteractiveTaskBytes = 64 << 10

type TaskResult struct {
	ExitCode int
	Detail   string
}

type TaskExecution interface {
	SessionID() string
	Run() TaskResult
	Cancel()
}

// TaskRunner is owned by the TUI. The application implementation prepares a
// single bounded run while retaining Provider, Policy, Store, and tool wiring.
type TaskRunner interface {
	PrepareTask(string) (TaskExecution, error)
	Close()
}

type interactivePhase uint8

const (
	phaseInput interactivePhase = iota
	phaseStarting
	phaseRunning
	phaseCancelling
	phaseDone
)

// InteractiveModel is the default TTY product shell. It submits user intent
// through TaskRunner and rebuilds visible state only from committed facts.
type InteractiveModel struct {
	info         brand.Info
	runner       TaskRunner
	factSource   SessionFactSource
	approvals    ApprovalSource
	input        textarea.Model
	viewport     viewport.Model
	view         session.SessionView
	phase        interactivePhase
	width        int
	height       int
	color        bool
	inputError   string
	feedError    error
	detail       string
	exitCode     int
	execution    TaskExecution
	subscription FactSubscription
	approval     *ApprovalPrompt
}

func NewInteractiveModel(info brand.Info, runner TaskRunner, factSource SessionFactSource, approvals ApprovalSource, color bool) InteractiveModel {
	input := textarea.New()
	input.Placeholder = "请描述要完成的 Coding 任务……"
	input.ShowLineNumbers = false
	input.CharLimit = MaxInteractiveTaskBytes
	input.MaxContentHeight = MaxInteractiveTaskBytes
	input.SetHeight(5)
	input.SetWidth(76)
	if !color {
		input.SetStyles(textarea.Styles{})
	}
	_ = input.Focus()
	view := viewport.New(viewport.WithWidth(76), viewport.WithHeight(8))
	model := InteractiveModel{
		info: info, runner: runner, factSource: factSource, approvals: approvals,
		input: input, viewport: view, phase: phaseInput, color: color,
	}
	model.refreshViewport(true)
	return model
}

func (m InteractiveModel) Init() tea.Cmd { return m.input.Focus() }

func (m InteractiveModel) ExitCode() int { return m.exitCode }

func (m InteractiveModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch value := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = value.Width, value.Height
		m.resize()
		return m, nil
	case tea.KeyPressMsg:
		return m.updateKey(value)
	case taskPreparedMsg:
		if value.err != nil {
			m.phase, m.inputError = phaseInput, value.err.Error()
			return m, m.input.Focus()
		}
		if value.execution == nil {
			m.phase, m.inputError = phaseInput, "任务运行入口返回了空执行对象"
			return m, m.input.Focus()
		}
		m.execution = value.execution
		m.phase = phaseRunning
		m.view = session.SessionView{
			ID: value.execution.SessionID(), ProjectRoot: m.info.WorkDir, Status: "starting",
		}
		m.refreshViewport(true)
		commands := []tea.Cmd{runTask(value.execution), waitForApproval(m.approvals)}
		if m.factSource != nil {
			commands = append(commands, startFactFeed(m.factSource, m.view.ID, 0))
		}
		return m, tea.Batch(commands...)
	case taskResultMsg:
		m.phase, m.exitCode, m.detail = phaseDone, value.result.ExitCode, strings.TrimSpace(value.result.Detail)
		m.execution, m.approval = nil, nil
		m.refreshViewport(true)
		if m.factSource != nil && m.view.ID != "" {
			return m, replayFacts(m.factSource, m.view.ID, m.view.LastSeq)
		}
		return m, nil
	case approvalPromptMsg:
		if m.phase == phaseDone || !value.prompt.active() {
			return m, nil
		}
		m.approval = &value.prompt
		m.refreshViewport(false)
		return m, nil
	case factFeedStartedMsg:
		m.subscription = value.subscription
		return m.applyFactPage(value.page)
	case factReplayMsg:
		return m.applyFactPage(value.page)
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
		m.refreshViewport(false)
		return m, waitForFact(m.subscription)
	case factFeedErrorMsg:
		m.feedError = value.err
		m.closeSubscription()
		return m, nil
	}
	if m.phase != phaseInput && m.approval == nil {
		var command tea.Cmd
		m.viewport, command = m.viewport.Update(message)
		return m, command
	}
	return m, nil
}

func (m InteractiveModel) updateKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	name := key.String()
	if m.approval != nil {
		switch name {
		case "y":
			return m.resolveApproval(policy.ApprovalApprove, "")
		case "n":
			return m.resolveApproval(policy.ApprovalReject, "用户在 TUI 中拒绝")
		case "e":
			return m.resolveApproval(policy.ApprovalEdit, "用户要求编辑后重新准备")
		case "ctrl+c":
			m.cancelTask()
			m.approval = nil
			return m, nil
		}
		return m, nil
	}
	switch m.phase {
	case phaseInput:
		switch name {
		case "esc":
			m.close()
			return m, tea.Quit
		case "ctrl+s", "ctrl+enter":
			return m.submitTask()
		}
		var command tea.Cmd
		m.input, command = m.input.Update(key)
		return m, command
	case phaseStarting, phaseRunning:
		if name == "ctrl+c" || name == "q" {
			m.cancelTask()
		}
	case phaseCancelling:
		return m, nil
	case phaseDone:
		if name == "q" || name == "esc" || name == "ctrl+c" {
			m.close()
			return m, tea.Quit
		}
	}
	var command tea.Cmd
	m.viewport, command = m.viewport.Update(key)
	return m, command
}

func (m InteractiveModel) submitTask() (tea.Model, tea.Cmd) {
	task := strings.TrimSpace(m.input.Value())
	switch {
	case task == "":
		m.inputError = "任务描述不能为空"
		return m, nil
	case !utf8.ValidString(task):
		m.inputError = "任务描述必须是有效 UTF-8 文本"
		return m, nil
	case len(task) > MaxInteractiveTaskBytes:
		m.inputError = "任务描述超过 64 KiB 上限"
		return m, nil
	case m.runner == nil:
		m.inputError = "任务运行入口不可用"
		return m, nil
	}
	m.phase, m.inputError = phaseStarting, ""
	m.input.Blur()
	return m, prepareTask(m.runner, task)
}

func (m InteractiveModel) resolveApproval(choice policy.ApprovalChoice, reason string) (tea.Model, tea.Cmd) {
	if m.approval == nil {
		return m, nil
	}
	prompt := *m.approval
	m.approval = nil
	if !prompt.Resolve(policy.ApprovalResponse{Choice: choice, Reason: reason}) {
		m.detail = "审批请求已经失效，未执行任何额外操作"
	}
	m.refreshViewport(false)
	return m, waitForApproval(m.approvals)
}

func (m *InteractiveModel) cancelTask() {
	if m.execution != nil {
		m.execution.Cancel()
		m.phase, m.detail = phaseCancelling, "正在取消；等待 Runtime 写入确定终态……"
	}
}

func (m InteractiveModel) applyFactPage(page session.PublicFactPage) (tea.Model, tea.Cmd) {
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
	m.refreshViewport(false)
	if page.More {
		return m, replayFacts(m.factSource, m.view.ID, m.view.LastSeq)
	}
	return m, waitForFact(m.subscription)
}

func (m *InteractiveModel) closeSubscription() {
	if m.subscription != nil {
		m.subscription.Close()
		m.subscription = nil
	}
}

func (m *InteractiveModel) close() {
	m.closeSubscription()
	if m.runner != nil {
		m.runner.Close()
	}
	if m.approvals != nil {
		m.approvals.Close()
	}
}

type taskPreparedMsg struct {
	execution TaskExecution
	err       error
}

type taskResultMsg struct{ result TaskResult }
type approvalPromptMsg struct{ prompt ApprovalPrompt }

func prepareTask(runner TaskRunner, task string) tea.Cmd {
	return func() tea.Msg {
		execution, err := runner.PrepareTask(task)
		return taskPreparedMsg{execution: execution, err: err}
	}
}

func runTask(execution TaskExecution) tea.Cmd {
	return func() tea.Msg { return taskResultMsg{result: execution.Run()} }
}

func waitForApproval(source ApprovalSource) tea.Cmd {
	if source == nil {
		return nil
	}
	return func() tea.Msg {
		prompt, open := <-source.Prompts()
		if !open {
			return nil
		}
		return approvalPromptMsg{prompt: prompt}
	}
}

func (m *InteractiveModel) resize() {
	width := m.width
	if width <= 0 {
		width = 80
	}
	contentWidth := max(20, width-4)
	m.input.SetWidth(contentWidth)
	m.input.SetHeight(5)
	m.viewport.SetWidth(contentWidth)
	height := m.height
	if height <= 0 {
		height = 24
	}
	reserved := lipgloss.Height(m.renderHeader()) + lipgloss.Height(m.renderAction()) + 3
	m.viewport.SetHeight(max(3, height-reserved))
	m.refreshViewport(false)
}

func (m *InteractiveModel) refreshViewport(forceBottom bool) {
	wasBottom := forceBottom || m.viewport.AtBottom()
	if m.view.ID == "" {
		m.viewport.SetContent(brand.Mark + "\n\n不是记得更多，而是记得更对。\n\n输入一个明确任务。梦蝶会在本地受控边界内读取、修改并验证当前项目。\n持久事实是恢复依据；界面和实时通知都可以重建。")
	} else {
		m.viewport.SetContent(renderSessionTimeline(m.view, max(20, m.viewport.Width())))
	}
	if wasBottom {
		m.viewport.GotoBottom()
	}
}

func (m InteractiveModel) View() tea.View {
	content := strings.Join([]string{m.renderHeader(), m.viewport.View(), m.renderAction()}, "\n")
	result := tea.NewView(content)
	result.AltScreen = true
	result.WindowTitle = "MengDie Code / 梦蝶 Code"
	return result
}

func (m InteractiveModel) renderHeader() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	heading := "MengDie Code / 梦蝶 Code"
	if m.color {
		heading = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Render(heading)
	}
	status := interactiveStatus(m.phase)
	if width < 72 {
		valueWidth := max(12, width-5)
		return strings.Join([]string{
			fmt.Sprintf("%s  %s", heading, m.info.Version),
			"项目 " + clip(m.info.WorkDir, valueWidth),
			"模型 " + clip(m.info.Model, valueWidth),
			"安全 " + clip(m.info.Security, valueWidth),
			"状态 " + status,
		}, "\n")
	}
	return strings.Join([]string{
		fmt.Sprintf("%s  %s", heading, m.info.Version),
		fmt.Sprintf("项目 %s · 模型 %s · 状态 %s", clip(m.info.WorkDir, max(12, width/2)), clip(m.info.Model, 32), status),
		"安全 " + clip(m.info.Security, max(12, width-5)),
	}, "\n")
}

func (m InteractiveModel) renderAction() string {
	if m.feedError != nil {
		return "事实流异常（已提交内容仍可恢复）：" + clip(m.feedError.Error(), 72)
	}
	if m.approval != nil {
		request := m.approval.Request
		preview := strings.TrimSpace(strings.Join([]string{request.Preview.Title, request.Preview.Body}, "\n"))
		preview = clipLines(preview, 8, 120)
		return fmt.Sprintf("需要审批 · %s · 风险 %s\n%s\n[y] 允许  [n] 拒绝  [e] 编辑后重新准备  [Ctrl+C] 取消任务", request.Tool, request.Risk, preview)
	}
	switch m.phase {
	case phaseInput:
		inputView := m.input.View()
		if !m.color {
			inputView = ansi.Strip(inputView)
		}
		message := inputView + "\n[Ctrl+S / Ctrl+Enter] 提交  [Enter] 换行  [Esc] 退出"
		if m.inputError != "" {
			message += "\n" + m.inputError
		}
		return message
	case phaseStarting, phaseRunning:
		return "任务运行中 · [PgUp/PgDn] 查看时间线 · [Ctrl+C 或 q] 安全取消"
	case phaseCancelling:
		return "正在取消并等待持久终态，请勿强制关闭……"
	case phaseDone:
		message := "任务已结束 · [PgUp/PgDn] 查看时间线 · [q/Esc] 退出"
		if m.detail != "" {
			message += "\n" + clip(m.detail, 120)
		}
		return message
	default:
		return ""
	}
}

func interactiveStatus(phase interactivePhase) string {
	switch phase {
	case phaseInput:
		return "等待任务"
	case phaseStarting:
		return "正在启动"
	case phaseRunning:
		return "运行中"
	case phaseCancelling:
		return "正在取消"
	case phaseDone:
		return "已结束"
	default:
		return "未知"
	}
}

func clipLines(value string, lines, width int) string {
	if strings.TrimSpace(value) == "" {
		return "（无预览文本）"
	}
	parts := strings.Split(value, "\n")
	if len(parts) > lines {
		parts = append(parts[:lines], "…")
	}
	for index := range parts {
		parts[index] = clip(parts[index], width)
	}
	return strings.Join(parts, "\n")
}
