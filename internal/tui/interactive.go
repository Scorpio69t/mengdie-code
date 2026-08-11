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

const (
	wideLayoutWidth = 96
	sidebarWidth    = 31
	maxCanvasWidth  = 144
	maxReadingWidth = 88
)

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
	task         string
	exitCode     int
	execution    TaskExecution
	subscription FactSubscription
	approval     *ApprovalPrompt
}

func NewInteractiveModel(info brand.Info, runner TaskRunner, factSource SessionFactSource, approvals ApprovalSource, color bool) InteractiveModel {
	input := textarea.New()
	input.Prompt = ""
	input.Placeholder = "描述要完成的编码任务……"
	input.ShowLineNumbers = false
	input.CharLimit = MaxInteractiveTaskBytes
	input.MaxContentHeight = MaxInteractiveTaskBytes
	input.SetHeight(2)
	input.SetWidth(76)
	input.SetStyles(textarea.Styles{})
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
	m.task = task
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
	contentWidth := m.contentWidth()
	mainWidth, _ := m.layoutWidths()
	m.input.SetWidth(max(16, contentWidth-6))
	m.input.SetHeight(2)
	m.viewport.SetWidth(mainWidth)
	height := m.height
	if height <= 0 {
		height = 30
	}
	reserved := lipgloss.Height(m.renderHeader()) + lipgloss.Height(m.renderAction()) + 2
	m.viewport.SetHeight(max(3, height-reserved))
	m.refreshViewport(false)
}

func (m *InteractiveModel) refreshViewport(forceBottom bool) {
	wasBottom := forceBottom || m.viewport.AtBottom()
	if m.view.ID == "" {
		m.viewport.SetContent(m.renderWelcome())
		m.viewport.GotoTop()
		return
	} else {
		m.viewport.SetContent(m.renderConversation())
	}
	if wasBottom {
		m.viewport.GotoBottom()
	}
}

func (m InteractiveModel) View() tea.View {
	content := strings.Join([]string{m.renderHeader(), m.renderMain(), m.renderAction()}, "\n")
	if m.width > 4 {
		content = lipgloss.NewStyle().MarginLeft(m.canvasMargin()).Render(content)
	}
	result := tea.NewView(content)
	result.AltScreen = true
	result.WindowTitle = "MengDie Code / 梦蝶 Code"
	return result
}

func (m InteractiveModel) renderHeader() string {
	styles := newInteractiveStyles(m.color)
	width := m.contentWidth()
	title := strings.Join([]string{
		styles.accent.Render(brand.CompactMark),
		styles.strong.Render("梦蝶 Code"),
	}, "  ")
	right := styles.status.Render(statusSymbol(m.phase) + " " + interactiveStatus(m.phase))
	if width < 60 {
		return strings.Join([]string{
			padBetween(title, right, width),
			styles.muted.Render(truncateLine("项目  "+projectName(m.info.WorkDir), width)),
			styles.muted.Render(truncateLine("模型  "+m.info.Model, width)),
			styles.muted.Render(truncateLine("安全  "+m.info.Security, width)),
		}, "\n")
	}
	left := title + "  " + styles.muted.Render(m.info.Version)
	first := padBetween(left, right, width)
	if width >= wideLayoutWidth {
		return first
	}
	context := strings.Join([]string{
		projectName(m.info.WorkDir),
		m.info.Model,
		m.info.Security,
	}, "  ·  ")
	return first + "\n" + styles.muted.Render(truncateLine(context, width))
}

func (m InteractiveModel) renderAction() string {
	styles := newInteractiveStyles(m.color)
	width := m.contentWidth()
	if m.feedError != nil {
		return renderBottomBar(styles, width, "事实流已暂停", "已提交内容仍可恢复 · "+clip(m.feedError.Error(), max(20, width-24)))
	}
	if m.approval != nil {
		request := m.approval.Request
		preview := strings.TrimSpace(strings.Join([]string{request.Preview.Title, request.Preview.Body}, "\n"))
		preview = clipLines(preview, 7, max(24, width-6))
		body := strings.Join([]string{
			padBetween(styles.strong.Render("需要你的决定"), styles.muted.Render(request.Tool+" · 风险 "+request.Risk), max(20, width-6)),
			preview,
			styles.accent.Render("Y 允许") + "   " + styles.muted.Render("N 拒绝  ·  E 编辑后重备  ·  Ctrl+C 取消"),
		}, "\n")
		return styles.approval.Render(body)
	}
	switch m.phase {
	case phaseInput:
		inputView := m.input.View()
		if !m.color {
			inputView = ansi.Strip(inputView)
		}
		message := inputView + "\n" + padBetween(
			styles.muted.Render("Enter 换行  ·  Esc 退出"),
			styles.accent.Render("Ctrl+S 提交"),
			max(20, width-6),
		)
		if m.inputError != "" {
			message += "\n" + styles.strong.Render(m.inputError)
		}
		return styles.input.Render(message)
	case phaseStarting, phaseRunning:
		return renderBottomBar(styles, width, statusSymbol(m.phase)+" "+interactiveStatus(m.phase), "PgUp/PgDn 时间线  ·  Ctrl+C 或 q 安全取消")
	case phaseCancelling:
		return renderBottomBar(styles, width, "正在取消", "等待 Runtime 写入持久终态，请勿强制关闭")
	case phaseDone:
		message := "PgUp/PgDn 时间线  ·  q 或 Esc 退出"
		if m.detail != "" {
			message = clip(m.detail, max(20, width-18)) + "  ·  " + message
		}
		return renderBottomBar(styles, width, statusSymbol(m.phase)+" "+interactiveStatus(m.phase), message)
	default:
		return ""
	}
}

func (m InteractiveModel) renderMain() string {
	mainWidth, sideWidth := m.layoutWidths()
	main := lipgloss.NewStyle().Width(mainWidth).Height(m.viewport.Height()).Render(m.viewport.View())
	if sideWidth == 0 {
		return main
	}
	styles := newInteractiveStyles(m.color)
	sidebarContentWidth := max(12, sideWidth-7)
	sidebar := styles.sidebar.Width(max(16, sideWidth-4)).Height(m.viewport.Height()).Render(m.renderSidebar(sidebarContentWidth))
	return lipgloss.JoinHorizontal(lipgloss.Top, main, "  ", sidebar)
}

func (m InteractiveModel) renderWelcome() string {
	styles := newInteractiveStyles(m.color)
	width := min(maxReadingWidth, max(20, m.viewport.Width()-2))
	if m.viewport.Width() < 48 || m.viewport.Height() < 14 {
		return strings.Join([]string{
			styles.label.Render("开始一个任务"),
			styles.strong.Render("把目标说清楚，剩下的交给梦蝶。"),
			"",
			ansi.Wrap("描述目标、报错或验收条件；梦蝶会在当前项目的受控边界内工作。", width, " "),
			"",
			styles.accent.Render("不是记得更多，而是记得更对。"),
		}, "\n")
	}
	lines := []string{
		styles.label.Render("开始一个任务"),
		styles.strong.Render("把目标说清楚，剩下的交给梦蝶。"),
		ansi.Wrap("可以附上报错、文件名或验收条件。梦蝶会在当前项目的受控边界内读取、修改并验证；界面始终以已提交事实为准。", width, " "),
		"",
		styles.label.Render("试试这样说"),
		styles.muted.Render("  修复当前失败的测试"),
		styles.muted.Render("  解释这个报错，并给出最小修改"),
		styles.muted.Render("  检查最近改动并补充测试"),
		"",
		styles.accent.Render("不是记得更多，而是记得更对。"),
	}
	topPadding := min(14, max(1, (m.viewport.Height()-len(lines))/2))
	lines = append(make([]string, topPadding), lines...)
	return strings.Join(lines, "\n")
}

func (m InteractiveModel) renderConversation() string {
	styles := newInteractiveStyles(m.color)
	width := min(maxReadingWidth, max(20, m.viewport.Width()-1))
	var sections []string
	if strings.TrimSpace(m.task) != "" {
		sections = append(sections, styles.muted.Render("你")+"\n"+ansi.Wrap(strings.TrimSpace(m.task), width, " "))
	}
	for _, message := range m.view.Messages {
		text := strings.TrimSpace(message.Text)
		if text == "" {
			continue
		}
		sections = append(sections, styles.accent.Render(brand.CompactMark+"  梦蝶")+"\n"+ansi.Wrap(text, width, " "))
	}
	if len(m.view.Tools) > 0 {
		lines := []string{styles.muted.Render("工具活动")}
		for _, tool := range m.view.Tools {
			line := "└─ " + tool.Tool + "  " + toolPhaseLabel(tool.Phase)
			if strings.TrimSpace(tool.Summary) != "" {
				line += "  ·  " + strings.TrimSpace(tool.Summary)
			}
			lines = append(lines, styles.muted.Render(truncateLine(line, width)))
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	for _, warning := range m.view.Warnings {
		sections = append(sections, styles.strong.Render("提示")+"\n"+ansi.Wrap(warning.Message, width, " "))
	}
	if len(sections) == 0 {
		return styles.muted.Render("正在建立上下文……")
	}
	return strings.Join(sections, "\n\n")
}

func (m InteractiveModel) renderSidebar(width int) string {
	styles := newInteractiveStyles(m.color)
	section := func(label, value string) string {
		return styles.label.Render(label) + "\n" + value
	}
	sessionID := "新任务"
	if m.view.ID != "" {
		sessionID = truncateLine(m.view.ID, width)
	}
	completed, total := toolProgress(m.view.Tools)
	progress := fmt.Sprintf("%d 条事实  ·  工具 %d/%d", m.view.LastSeq, completed, total)
	parts := []string{
		section("工作区", styles.strong.Render(truncateLine(projectName(m.info.WorkDir), width))+"\n"+styles.muted.Render(truncateLine(m.info.WorkDir, width))),
		section("会话", truncateLine(sessionID, width)),
		section("模型", truncateLine(m.info.Model, width)),
		section("安全", truncateLine(m.info.Security, width)),
		section("进度", truncateLine(progress, width)),
	}
	if len(m.view.Todos) > 0 {
		lines := make([]string, 0, min(4, len(m.view.Todos)))
		for index, todo := range m.view.Todos {
			if index == 4 {
				lines = append(lines, "…")
				break
			}
			lines = append(lines, "· "+truncateLine(todo.Content, max(10, width-2)))
		}
		parts = append(parts, section("待办", strings.Join(lines, "\n")))
	}
	return strings.Join(parts, "\n\n")
}

func (m InteractiveModel) contentWidth() int {
	width := m.width
	if width <= 0 {
		width = 100
	}
	return min(maxCanvasWidth, max(20, width-4))
}

func (m InteractiveModel) canvasMargin() int {
	width := m.width
	if width <= 0 {
		width = 100
	}
	return max(0, (width-m.contentWidth())/2)
}

func (m InteractiveModel) layoutWidths() (int, int) {
	width := m.contentWidth()
	if width < wideLayoutWidth {
		return width, 0
	}
	return max(40, width-sidebarWidth-2), sidebarWidth
}

type interactiveStyles struct {
	accent   lipgloss.Style
	strong   lipgloss.Style
	muted    lipgloss.Style
	label    lipgloss.Style
	status   lipgloss.Style
	input    lipgloss.Style
	approval lipgloss.Style
	sidebar  lipgloss.Style
}

func newInteractiveStyles(color bool) interactiveStyles {
	styles := interactiveStyles{
		input:    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
		approval: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
		sidebar:  lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).PaddingLeft(2),
	}
	if !color {
		return styles
	}
	accent := lipgloss.Color("#2CC7A1")
	muted := lipgloss.Color("#7D8590")
	border := lipgloss.Color("#30363D")
	styles.accent = lipgloss.NewStyle().Foreground(accent).Bold(true)
	styles.strong = lipgloss.NewStyle().Foreground(lipgloss.Color("#E6EDF3")).Bold(true)
	styles.muted = lipgloss.NewStyle().Foreground(muted)
	styles.label = lipgloss.NewStyle().Foreground(muted).Bold(true)
	styles.status = lipgloss.NewStyle().Foreground(accent)
	styles.input = styles.input.BorderForeground(border)
	styles.approval = styles.approval.BorderForeground(accent)
	styles.sidebar = styles.sidebar.BorderForeground(border)
	return styles
}

func renderBottomBar(styles interactiveStyles, width int, left, right string) string {
	content := padBetween(styles.status.Render(left), styles.muted.Render(right), max(20, width-4))
	bar := lipgloss.NewStyle().Border(lipgloss.NormalBorder(), true, false, false, false).Padding(0, 1)
	if styles.sidebar.GetBorderLeftForeground() != nil {
		bar = bar.BorderForeground(lipgloss.Color("#30363D"))
	}
	return bar.Render(content)
}

func padBetween(left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	right = truncateLine(right, width)
	rightWidth := ansi.StringWidth(right)
	left = truncateLine(left, max(0, width-rightWidth-1))
	space := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if space < 1 {
		return truncateLine(left+right, width)
	}
	return left + strings.Repeat(" ", space) + right
}

func truncateLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(value, width, "…")
}

func projectName(path string) string {
	path = strings.TrimRight(strings.ReplaceAll(path, "\\", "/"), "/")
	if index := strings.LastIndex(path, "/"); index >= 0 && index+1 < len(path) {
		return path[index+1:]
	}
	if path == "" {
		return "当前项目"
	}
	return path
}

func statusSymbol(phase interactivePhase) string {
	if phase == phaseRunning || phase == phaseStarting {
		return "●"
	}
	return "○"
}

func toolProgress(tools []session.ToolView) (int, int) {
	completed := 0
	for _, tool := range tools {
		if tool.Phase == "completed" {
			completed++
		}
	}
	return completed, len(tools)
}

func toolPhaseLabel(phase string) string {
	switch phase {
	case "completed":
		return "已完成"
	case "started":
		return "执行中"
	case "proposed":
		return "待决策"
	default:
		return phase
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
		parts[index] = truncateLine(parts[index], width)
	}
	return strings.Join(parts, "\n")
}
