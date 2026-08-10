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
}

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

	for _, approval := range view.Approvals {
		if strings.TrimSpace(approval.Decision) == "" {
			return blockResume(plan, fmt.Sprintf("Run %s 的工具调用 %s 仍在等待审批", approval.RunID, approval.CallID)), nil
		}
	}
	for _, tool := range view.Tools {
		if tool.Phase != "completed" {
			return blockResume(plan, fmt.Sprintf("Run %s 的工具调用 %s 状态为 %s，无法确认副作用边界", tool.RunID, tool.CallID, tool.Phase)), nil
		}
	}

	history := make([]provider.Message, 0, len(contextMessages))
	assistantContexts := make([]ContextMessage, 0)
	toolContexts := make(map[string]ContextMessage)
	pendingCalls := make(map[string]string)
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
			}
		case provider.RoleTool:
			runID, ok := pendingCalls[message.ToolCallID]
			if !ok || runID != item.RunID {
				return blockResume(plan, fmt.Sprintf("工具结果 %s 没有同 Run 的 Assistant 调用", message.ToolCallID)), nil
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
	if len(pendingCalls) != 0 {
		return blockResume(plan, "最后一个模型边界仍有未完成的工具调用"), nil
	}
	if err := validateResumePublicBoundaries(view, assistantContexts, toolContexts, runUsers); err != nil {
		return blockResume(plan, err.Error()), nil
	}
	if err := (provider.ChatRequest{Model: "resume-validation", Messages: history}).Validate(); err != nil {
		return blockResume(plan, fmt.Sprintf("私有上下文无法构成有效模型请求：%v", err)), nil
	}
	todos, err := resumeTodos(view)
	if err != nil {
		return blockResume(plan, err.Error()), nil
	}
	plan.History, plan.Todos, plan.CanResume = history, todos, true
	return plan, nil
}

func validateResumePublicBoundaries(view SessionView, assistants []ContextMessage, toolContexts map[string]ContextMessage, runUsers map[string]int) error {
	if len(assistants) != len(view.Messages) {
		return fmt.Errorf("assistant 私有边界数 %d 与公开完成消息数 %d 不一致", len(assistants), len(view.Messages))
	}
	for index, public := range view.Messages {
		private := assistants[index]
		if private.RunID != public.RunID || private.Message.Content != public.Text {
			return fmt.Errorf("第 %d 条 Assistant 私有边界与公开事实不一致", index+1)
		}
	}
	if len(toolContexts) != len(view.Tools) {
		return fmt.Errorf("工具私有结果数 %d 与公开完成事实数 %d 不一致", len(toolContexts), len(view.Tools))
	}
	for _, public := range view.Tools {
		private, ok := toolContexts[resumeCallKey(public.RunID, public.CallID)]
		if !ok || private.Message.Name != public.Tool {
			return fmt.Errorf("run %s 的工具调用 %s 私有结果与公开事实不一致", public.RunID, public.CallID)
		}
	}
	for _, run := range view.Runs {
		if runUsers[run.ID] != 1 {
			return fmt.Errorf("run %s 没有且仅有一个完整用户起始边界", run.ID)
		}
	}
	return nil
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
