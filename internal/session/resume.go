// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

const DefaultResumeMessage = "请基于已有上下文和当前项目状态继续完成任务；执行任何副作用前先重新验证。"

// ResumePlan is the public safety decision plus private runtime inputs. JSON
// output deliberately excludes the model transcript and Todo contents.
type ResumePlan struct {
	SessionID        string `json:"session_id"`
	CanResume        bool   `json:"can_resume"`
	Reason           string `json:"reason,omitempty"`
	SourceStatus     string `json:"source_status"`
	LastSeq          uint64 `json:"last_seq"`
	ContextOrdinal   uint64 `json:"context_ordinal"`
	PriorRuns        int    `json:"prior_runs"`
	SanitizedResults int    `json:"sanitized_results"`

	History                []provider.Message `json:"-"`
	Todos                  []tools.Todo       `json:"-"`
	ExpectedSessionSeq     uint64             `json:"-"`
	ExpectedContextOrdinal uint64             `json:"-"`
	Recovery               *RecoveryAction    `json:"-"`
}

// RecoveryAction identifies one interrupted call that may be safely retried.
// Its ToolCall comes only from the private, integrity-checked context ledger.
type RecoveryAction struct {
	SourceRunID string
	Call        provider.ToolCall
	Kind        string
}

const (
	RecoveryReapprove = "reapprove"
	RecoveryRetryRead = "retry_read"
)

// MatchResumeCommand lets the application honor an already-registered
// idempotency key before re-analyzing mutable Session state. It compares only
// private ledger facts and project identity; no payload is returned.
func (s *Service) MatchResumeCommand(ctx context.Context, commandID, sessionID, message, projectRoot string) (bool, error) {
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return false, nil
	}
	wantPayload, err := ResumeCommandPayload(sessionID, message)
	if err != nil {
		return false, err
	}
	wantDigest, err := commandPayloadDigest(wantPayload)
	if err != nil {
		return false, err
	}
	command, err := s.store.LookupCommand(ctx, commandID)
	if errors.Is(err, ErrCommandNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	base, err := s.loadBase(ctx, command.SessionID)
	if err != nil {
		return false, err
	}
	absoluteRoot, err := filepath.Abs(strings.TrimSpace(projectRoot))
	if err != nil {
		return false, fmt.Errorf("resolve resume command project root: %w", err)
	}
	if command.SessionID != strings.TrimSpace(sessionID) || command.Kind != CommandKindResume ||
		command.PayloadSHA256 != wantDigest || base.ProjectIdentity != projectIdentity(filepath.Clean(absoluteRoot)) {
		return false, ErrCommandConflict
	}
	return true, nil
}

// AnalyzeResume combines public replay facts with the private context chain.
// Expected recovery hazards are returned as CanResume=false, while inability
// to read the source Session remains an operational error.
func (s *Service) AnalyzeResume(ctx context.Context, sessionID, projectRoot string) (ResumePlan, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ResumePlan{}, errors.New("resume session id is required")
	}
	view, err := s.View(ctx, sessionID)
	if err != nil {
		return ResumePlan{}, err
	}
	plan := ResumePlan{
		SessionID: sessionID, SourceStatus: view.Status, LastSeq: view.LastSeq,
		ExpectedSessionSeq: view.LastSeq, PriorRuns: len(view.Runs),
	}
	absoluteRoot, err := filepath.Abs(strings.TrimSpace(projectRoot))
	if err != nil {
		return ResumePlan{}, fmt.Errorf("resolve resume project root: %w", err)
	}
	if view.ProjectIdentity != projectIdentity(filepath.Clean(absoluteRoot)) {
		return blockResume(plan, "该会话属于另一个项目，拒绝跨项目恢复"), nil
	}

	contextMessages, err := s.store.LoadContext(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrContextCorrupt) {
			return blockResume(plan, "私有上下文日志损坏或不连续，无法证明恢复安全"), nil
		}
		return ResumePlan{}, err
	}
	if len(contextMessages) == 0 {
		return blockResume(plan, "该会话没有私有上下文日志（旧版会话不可恢复）"), nil
	}
	plan.ContextOrdinal = contextMessages[len(contextMessages)-1].Ordinal
	plan.ExpectedContextOrdinal = plan.ContextOrdinal

	recovered, recoveryRuns, err := recoveryFacts(view)
	if err != nil {
		return blockResume(plan, err.Error()), nil
	}
	recovery, err := selectRecovery(view, recovered)
	if err != nil {
		return blockResume(plan, err.Error()), nil
	}

	history := make([]provider.Message, 0, len(contextMessages))
	assistantContexts := make([]ContextMessage, 0)
	toolContexts := make(map[string]ContextMessage)
	pendingCalls := make(map[string]string)
	pendingToolCalls := make(map[string]provider.ToolCall)
	runUsers := make(map[string]int)
	for _, item := range contextMessages {
		message := item.Message
		switch message.Role {
		case provider.RoleUser:
			if len(pendingCalls) != 0 {
				return blockResume(plan, "上下文在工具结果完整写入前进入了新的用户消息"), nil
			}
			if item.Completeness != ContextFull {
				return blockResume(plan, "用户消息不是完整恢复边界"), nil
			}
			runUsers[item.RunID]++
			if runUsers[item.RunID] > 1 {
				return blockResume(plan, fmt.Sprintf("Run %s 存在多个用户起始边界", item.RunID)), nil
			}
		case provider.RoleAssistant:
			if len(pendingCalls) != 0 {
				return blockResume(plan, "上下文缺少上一条 Assistant 工具调用的结果"), nil
			}
			if item.Completeness != ContextFull {
				return blockResume(plan, "Assistant 消息不是完整恢复边界"), nil
			}
			assistantContexts = append(assistantContexts, item)
			for _, call := range message.ToolCalls {
				if _, duplicate := pendingCalls[call.ID]; duplicate {
					return blockResume(plan, fmt.Sprintf("工具调用 ID %s 重复", call.ID)), nil
				}
				pendingCalls[call.ID] = item.RunID
				pendingToolCalls[resumeCallKey(item.RunID, call.ID)] = call
			}
		case provider.RoleTool:
			runID, ok := pendingCalls[message.ToolCallID]
			if !ok {
				return blockResume(plan, fmt.Sprintf("工具结果 %s 没有 Assistant 调用", message.ToolCallID)), nil
			}
			if runID != item.RunID {
				if source, ok := recoveryRuns[item.RunID]; !ok || source != resumeCallKey(runID, message.ToolCallID) {
					return blockResume(plan, fmt.Sprintf("工具结果 %s 没有同 Run 的 Assistant 调用", message.ToolCallID)), nil
				}
			}
			key := resumeCallKey(item.RunID, message.ToolCallID)
			if _, duplicate := toolContexts[key]; duplicate {
				return blockResume(plan, fmt.Sprintf("工具结果 %s 重复", message.ToolCallID)), nil
			}
			toolContexts[key] = item
			delete(pendingCalls, message.ToolCallID)
			if item.Completeness == ContextSanitized {
				plan.SanitizedResults++
			}
		default:
			return blockResume(plan, fmt.Sprintf("上下文包含不支持的 %s 消息", message.Role)), nil
		}
		history = append(history, cloneProviderMessage(message))
	}
	if recovery != nil {
		call, ok := pendingToolCalls[resumeCallKey(recovery.SourceRunID, recovery.Call.ID)]
		if !ok {
			if _, resolved := recovered[resumeCallKey(recovery.SourceRunID, recovery.Call.ID)]; !resolved {
				return blockResume(plan, "恢复目标缺少私有 Assistant 工具调用"), nil
			}
		} else {
			recovery.Call = cloneToolCall(call)
			plan.Recovery = recovery
		}
	}
	if len(pendingCalls) != 0 {
		if plan.Recovery == nil || len(pendingCalls) != 1 || pendingCalls[plan.Recovery.Call.ID] != plan.Recovery.SourceRunID {
			return blockResume(plan, "最后一个模型边界仍有未完成的工具调用"), nil
		}
	}
	if err := validateResumePublicBoundaries(view, assistantContexts, toolContexts, runUsers, recoveryRuns); err != nil {
		return blockResume(plan, err.Error()), nil
	}
	historyForValidation := append([]provider.Message(nil), history...)
	if plan.Recovery != nil {
		historyForValidation = append(historyForValidation, provider.Message{
			Role: provider.RoleTool, ToolCallID: plan.Recovery.Call.ID, Name: plan.Recovery.Call.Name,
			Content: "恢复前将按当前状态重新执行该工具。",
		})
	}
	if err := (provider.ChatRequest{Model: "resume-validation", Messages: historyForValidation}).Validate(); err != nil {
		return blockResume(plan, fmt.Sprintf("私有上下文无法构成有效模型请求：%v", err)), nil
	}
	todos, err := resumeTodos(view)
	if err != nil {
		return blockResume(plan, err.Error()), nil
	}
	plan.History, plan.Todos, plan.CanResume = history, todos, true
	return plan, nil
}

func validateResumePublicBoundaries(view SessionView, assistants []ContextMessage, toolContexts map[string]ContextMessage, runUsers map[string]int, recoveryRuns map[string]string) error {
	if len(assistants) != len(view.Messages) {
		return fmt.Errorf("assistant 私有边界数 %d 与公开完成消息数 %d 不一致", len(assistants), len(view.Messages))
	}
	for index, public := range view.Messages {
		private := assistants[index]
		if private.RunID != public.RunID || private.Message.Content != public.Text {
			return fmt.Errorf("第 %d 条 Assistant 私有边界与公开事实不一致", index+1)
		}
	}
	completedTools := make([]ToolView, 0, len(view.Tools))
	for _, public := range view.Tools {
		if public.Phase == "completed" {
			completedTools = append(completedTools, public)
		}
	}
	if len(toolContexts) != len(completedTools) {
		return fmt.Errorf("工具私有结果数 %d 与公开完成事实数 %d 不一致", len(toolContexts), len(completedTools))
	}
	for _, public := range completedTools {
		private, ok := toolContexts[resumeCallKey(public.RunID, public.CallID)]
		if !ok || private.Message.Name != public.Tool {
			return fmt.Errorf("run %s 的工具调用 %s 私有结果与公开事实不一致", public.RunID, public.CallID)
		}
	}
	for _, run := range view.Runs {
		if runUsers[run.ID] == 0 {
			if _, recoveryOnly := recoveryRuns[run.ID]; recoveryOnly {
				continue
			}
		}
		if runUsers[run.ID] != 1 {
			return fmt.Errorf("run %s 没有且仅有一个完整用户起始边界", run.ID)
		}
	}
	return nil
}

func recoveryFacts(view SessionView) (map[string]RecoveryView, map[string]string, error) {
	bySource := make(map[string]RecoveryView, len(view.Recoveries))
	byRun := make(map[string]string, len(view.Recoveries))
	for _, item := range view.Recoveries {
		source := resumeCallKey(item.SourceRunID, item.CallID)
		if strings.TrimSpace(item.RunID) == "" || strings.TrimSpace(item.SourceRunID) == "" || strings.TrimSpace(item.CallID) == "" {
			return nil, nil, errors.New("恢复公开事实缺少身份")
		}
		if item.Action != RecoveryReapprove && item.Action != RecoveryRetryRead {
			return nil, nil, fmt.Errorf("恢复公开事实包含未知动作 %s", item.Action)
		}
		if item.Outcome != "completed" && item.Outcome != "failed" {
			return nil, nil, fmt.Errorf("恢复公开事实包含未知结果 %s", item.Outcome)
		}
		if _, duplicate := bySource[source]; duplicate {
			return nil, nil, fmt.Errorf("工具调用 %s 存在重复恢复事实", item.CallID)
		}
		if _, duplicate := byRun[item.RunID]; duplicate {
			return nil, nil, fmt.Errorf("run %s 存在多个恢复事实", item.RunID)
		}
		bySource[source], byRun[item.RunID] = item, source
	}
	return bySource, byRun, nil
}

func selectRecovery(view SessionView, recovered map[string]RecoveryView) (*RecoveryAction, error) {
	candidates := make([]RecoveryAction, 0, 1)
	pendingApprovals := make(map[string]struct{})
	add := func(action RecoveryAction) error {
		candidates = append(candidates, action)
		if len(candidates) > 1 {
			return errors.New("会话存在多个未完成工具调用，无法安全恢复")
		}
		return nil
	}
	for _, approval := range view.Approvals {
		key := resumeCallKey(approval.RunID, approval.CallID)
		if strings.TrimSpace(approval.Decision) != "" || recovered[key].CallID != "" {
			continue
		}
		if !hasResumeTool(view.Tools, approval.RunID, approval.CallID) {
			return nil, fmt.Errorf("run %s 的审批工具调用 %s 缺少公开工具事实", approval.RunID, approval.CallID)
		}
		if err := add(RecoveryAction{SourceRunID: approval.RunID, Call: provider.ToolCall{ID: approval.CallID}, Kind: RecoveryReapprove}); err != nil {
			return nil, err
		}
		pendingApprovals[key] = struct{}{}
	}
	for _, tool := range view.Tools {
		key := resumeCallKey(tool.RunID, tool.CallID)
		if tool.Phase == "completed" || recovered[key].CallID != "" {
			continue
		}
		if _, pendingApproval := pendingApprovals[key]; pendingApproval {
			continue
		}
		if tool.Phase != "running" {
			return nil, fmt.Errorf("run %s 的工具调用 %s 状态为 %s，无法确认副作用边界", tool.RunID, tool.CallID, tool.Phase)
		}
		if !recoveryReadOnly(tool.Effects) {
			return nil, fmt.Errorf("run %s 的工具调用 %s 包含副作用，恢复时保持阻断", tool.RunID, tool.CallID)
		}
		if err := add(RecoveryAction{SourceRunID: tool.RunID, Call: provider.ToolCall{ID: tool.CallID}, Kind: RecoveryRetryRead}); err != nil {
			return nil, err
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	return &candidates[0], nil
}

func hasResumeTool(items []ToolView, runID, callID string) bool {
	for _, item := range items {
		if item.RunID == runID && item.CallID == callID {
			return true
		}
	}
	return false
}

func recoveryReadOnly(effects []string) bool {
	if len(effects) == 0 {
		return false
	}
	for _, effect := range effects {
		if effect != string(tools.EffectRead) && effect != string(tools.EffectState) {
			return false
		}
	}
	return true
}

func resumeTodos(view SessionView) ([]tools.Todo, error) {
	result := make([]tools.Todo, 0, len(view.Todos))
	for _, item := range view.Todos {
		status := tools.TodoStatus(item.Status)
		switch status {
		case tools.TodoPending, tools.TodoInProgress, tools.TodoCompleted, tools.TodoCancelled:
		default:
			return nil, fmt.Errorf("todo %s 的状态 %s 无法恢复", item.ID, item.Status)
		}
		result = append(result, tools.Todo{ID: item.ID, Content: item.Content, Status: status})
	}
	return result, nil
}

func blockResume(plan ResumePlan, reason string) ResumePlan {
	plan.CanResume = false
	plan.Reason = reason
	plan.History = nil
	plan.Todos = nil
	return plan
}

func resumeCallKey(runID, callID string) string { return runID + "\x00" + callID }

func cloneProviderMessage(message provider.Message) provider.Message {
	clone := message
	clone.ToolCalls = append([]provider.ToolCall(nil), message.ToolCalls...)
	for index := range clone.ToolCalls {
		clone.ToolCalls[index].Arguments = append([]byte(nil), message.ToolCalls[index].Arguments...)
	}
	return clone
}

func cloneToolCall(call provider.ToolCall) provider.ToolCall {
	clone := call
	clone.Arguments = append([]byte(nil), call.Arguments...)
	return clone
}
