// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package agent implements the M1 single-agent, in-memory model/tool loop.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	agentcontext "github.com/Scorpio69t/mengdie-code/internal/context"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

const repeatedLimit = 3

type Options struct {
	Provider           provider.Provider
	Registry           *tools.Registry
	Guard              *platform.PathGuard
	Policy             *policy.Engine
	Broker             policy.Broker
	Now                func() time.Time
	SystemPrompt       string
	MaxContextTokens   int
	Environment        func() []string
	AllowedEnvironment []string
	Instructions       []agentcontext.Instruction
	ContextRecorder    ContextRecorder
	MutationJournal    tools.MutationJournal
}

// ContextRecorder persists private, model-visible message boundaries. The
// bool is false when output-bearing fields were replaced by a safe summary.
type ContextRecorder interface {
	RecordMessage(context.Context, provider.Message, bool) error
	RecordCompaction(context.Context, agentcontext.CompactionRecord) (agentcontext.CompactionReceipt, error)
}

type Agent struct {
	provider           provider.Provider
	registry           *tools.Registry
	guard              *platform.PathGuard
	policy             *policy.Engine
	broker             policy.Broker
	now                func() time.Time
	systemPrompt       string
	maxContextTokens   int
	environment        func() []string
	allowedEnvironment []string
	instructions       []agentcontext.Instruction
	contextRecorder    ContextRecorder
	mutationJournal    tools.MutationJournal
}

type RunRequest struct {
	RunID          string
	Task           string
	Model          string
	DisplayModel   string
	MaxTurns       int
	Security       string
	History        []provider.Message
	ContextSummary string
	Todos          []tools.Todo
	Recovery       *RecoveryAction
}

const (
	RecoveryReapprove   = "reapprove"
	RecoveryRetryRead   = "retry_read"
	RecoveryRetryWrite  = "retry_write"
	RecoveryVerifyWrite = "verify_write"
)

// RecoveryAction is a previously interrupted tool call selected by the
// Session safety analyzer. It is always re-prepared and re-authorized; the
// old source Run's capability is never available to a new Run.
type RecoveryAction struct {
	SourceRunID string
	Call        provider.ToolCall
	Kind        string
}

type RunResult struct {
	Summary     string
	Turns       int
	Usage       provider.Usage
	Todos       []tools.Todo
	DeniedTools int
}

func New(options Options) (*Agent, error) {
	switch {
	case options.Provider == nil:
		return nil, errors.New("agent: provider is required")
	case options.Registry == nil:
		return nil, errors.New("agent: tool registry is required")
	case options.Guard == nil:
		return nil, errors.New("agent: path guard is required")
	case options.Policy == nil:
		return nil, errors.New("agent: policy engine is required")
	case options.MaxContextTokens <= 0:
		return nil, errors.New("agent: max context tokens must be positive")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Environment == nil {
		options.Environment = os.Environ
	}
	return &Agent{
		provider: options.Provider, registry: options.Registry, guard: options.Guard,
		policy: options.Policy, broker: options.Broker, now: options.Now,
		systemPrompt: options.SystemPrompt, maxContextTokens: options.MaxContextTokens,
		environment:        options.Environment,
		allowedEnvironment: append([]string(nil), options.AllowedEnvironment...),
		instructions:       append([]agentcontext.Instruction(nil), options.Instructions...),
		contextRecorder:    options.ContextRecorder,
		mutationJournal:    options.MutationJournal,
	}, nil
}

func (a *Agent) Run(ctx context.Context, request RunRequest, emitter *events.Emitter) (RunResult, error) {
	if emitter == nil {
		return RunResult{}, errors.New("agent: event emitter is required")
	}
	if err := validateRunRequest(request); err != nil {
		return RunResult{}, err
	}
	state := &RunState{
		RunID: request.RunID, StartedAt: a.now(),
		Messages:           cloneMessages(request.History),
		CompactionMessages: cloneMessages(request.History),
		Summary:            strings.TrimSpace(request.ContextSummary),
		Todos:              append([]tools.Todo(nil), request.Todos...),
	}
	startContext := ctx
	if ctx.Err() != nil {
		startContext = context.WithoutCancel(ctx)
	}
	displayModel := request.DisplayModel
	if strings.TrimSpace(displayModel) == "" {
		displayModel = request.Model
	}
	if _, err := emitter.Emit(startContext, events.KindRunStarted, events.RunStarted{
		Model: displayModel, CWD: a.guard.Root(), Security: request.Security,
	}); err != nil {
		return RunResult{}, err
	}
	authorizer, err := policy.NewAuthorizer(policy.AuthorizerOptions{
		Engine: a.policy, Broker: a.broker, Observer: policy.EventObserver{Emitter: emitter}, Now: a.now,
	})
	if err != nil {
		return state.result(""), a.finishError(ctx, emitter, err)
	}
	if request.Recovery != nil {
		outcome := a.executeRecovered(ctx, state, emitter, authorizer, *request.Recovery)
		if outcome.fatal != nil {
			return state.result(""), outcome.fatal
		}
		if outcome.denied {
			state.recordDenial()
		}
		resumeMessage := provider.Message{
			Role: provider.RoleTool, ToolCallID: request.Recovery.Call.ID,
			Name: request.Recovery.Call.Name, Content: outcome.resumeMessage,
		}
		if err := a.recordContext(context.WithoutCancel(ctx), resumeMessage, outcome.resumeComplete); err != nil {
			return state.result(""), a.finishError(ctx, emitter, err)
		}
		state.appendMessageWithCompactionSource(provider.Message{
			Role: provider.RoleTool, ToolCallID: request.Recovery.Call.ID,
			Name: request.Recovery.Call.Name, Content: outcome.message,
		}, resumeMessage)
		if _, err := emitter.Emit(ctx, events.KindRecoveryResolved, events.RecoveryResolved{
			SourceRunID: request.Recovery.SourceRunID, CallID: request.Recovery.Call.ID,
			Action: request.Recovery.Kind, Outcome: recoveryOutcome(outcome),
		}); err != nil {
			return state.result(""), err
		}
	}
	userMessage := provider.Message{Role: provider.RoleUser, Content: request.Task}
	if err := a.recordContext(startContext, userMessage, true); err != nil {
		return state.result(""), a.finishError(ctx, emitter, err)
	}
	state.appendMessage(userMessage)
	if err := ctx.Err(); err != nil {
		return state.result(""), a.finishError(ctx, emitter, err)
	}

	capabilities, err := a.provider.Capabilities(ctx, request.Model)
	if err != nil {
		return state.result(""), a.finishError(ctx, emitter, err)
	}
	allToolSpecs := a.registry.Specs()
	baseToolSpecs := make([]tools.ToolSpec, 0, len(allToolSpecs))
	for _, spec := range allToolSpecs {
		if spec.Name != tools.ReadContextSourceToolName {
			baseToolSpecs = append(baseToolSpecs, spec)
		}
	}
	baseBuilder, err := agentcontext.NewBuilder(agentcontext.Options{
		Model: request.Model, SystemPrompt: a.systemPrompt,
		MaxContextTokens: a.maxContextTokens, Capabilities: capabilities,
		Tools:        baseToolSpecs,
		Instructions: a.instructions,
	})
	if err != nil {
		return state.result(""), a.finishError(ctx, emitter, err)
	}
	summaryBuilder := baseBuilder
	if len(baseToolSpecs) != len(allToolSpecs) {
		summaryBuilder, err = agentcontext.NewBuilder(agentcontext.Options{
			Model: request.Model, SystemPrompt: a.systemPrompt,
			MaxContextTokens: a.maxContextTokens, Capabilities: capabilities,
			Tools:        allToolSpecs,
			Instructions: a.instructions,
		})
		if err != nil {
			return state.result(""), a.finishError(ctx, emitter, err)
		}
	}

	tracker := repetitionTracker{}
	for state.Turn < request.MaxTurns {
		if err := ctx.Err(); err != nil {
			return state.result(""), a.finishError(ctx, emitter, err)
		}
		state.Turn++
		messages, sourceMessages, todos, summary := state.snapshot()
		contextState := agentcontext.State{
			Messages: messages, SourceMessages: sourceMessages, Todos: todos, Summary: summary,
		}
		builder := baseBuilder
		if summary != "" {
			builder = summaryBuilder
		}
		chatRequest, err := builder.Build(contextState)
		if errors.Is(err, agentcontext.ErrBudgetExceeded) {
			chatRequest, err = a.compactContext(ctx, state, emitter, summaryBuilder, contextState, request.Model)
		}
		if err != nil {
			return state.result(""), a.finishError(ctx, emitter, err)
		}
		observer := &streamObserver{emitter: emitter, state: state}
		response, err := a.provider.Stream(ctx, chatRequest, observer)
		if err != nil {
			return state.result(""), a.finishError(ctx, emitter, err)
		}
		if err := validateResponse(response); err != nil {
			return state.result(""), a.finishError(ctx, emitter, err)
		}
		if state.setUsage(response.Usage) {
			if err := observer.emitUsage(ctx, response.Usage); err != nil {
				return state.result(""), err
			}
		}
		if err := a.recordContext(ctx, response.Message, true); err != nil {
			return state.result(""), a.finishError(ctx, emitter, err)
		}
		state.appendMessage(response.Message)
		if _, err := emitter.Emit(ctx, events.KindMessageCompleted, events.MessageCompleted{Text: response.Message.Content}); err != nil {
			return state.result(""), err
		}
		if len(response.Message.ToolCalls) == 0 {
			summary := strings.TrimSpace(response.Message.Content)
			result := state.result(summary)
			if _, err := emitter.Emit(ctx, events.KindRunCompleted, events.RunCompleted{
				Summary: summary, DeniedTools: result.DeniedTools,
			}); err != nil {
				return result, err
			}
			return result, nil
		}

		for _, call := range response.Message.ToolCalls {
			callKey, err := canonicalCallKey(call)
			if err != nil {
				return state.result(""), a.finishError(ctx, emitter, err)
			}
			if tracker.observeCall(callKey) >= repeatedLimit {
				return state.result(""), a.finishError(ctx, emitter, ErrRepeatedCall)
			}
			outcome := a.executeOne(ctx, state, emitter, authorizer, call)
			if outcome.fatal != nil {
				return state.result(""), outcome.fatal
			}
			if outcome.denied {
				state.recordDenial()
			}
			resumeMessage := provider.Message{
				Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Name, Content: outcome.resumeMessage,
			}
			if err := a.recordContext(context.WithoutCancel(ctx), resumeMessage, outcome.resumeComplete); err != nil {
				return state.result(""), a.finishError(ctx, emitter, err)
			}
			state.appendMessageWithCompactionSource(provider.Message{
				Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Name, Content: outcome.message,
			}, resumeMessage)
			if outcome.failed {
				if tracker.observeFailure(outcome.failureKey) >= repeatedLimit {
					return state.result(""), a.finishError(ctx, emitter, ErrRepeatedFailure)
				}
			} else {
				tracker.resetFailure()
			}
		}
	}
	return state.result(""), a.finishError(ctx, emitter, ErrMaxTurns)
}

func (a *Agent) compactContext(
	ctx context.Context,
	state *RunState,
	emitter *events.Emitter,
	builder *agentcontext.Builder,
	contextState agentcontext.State,
	model string,
) (provider.ChatRequest, error) {
	if a.contextRecorder == nil {
		return provider.ChatRequest{}, fmt.Errorf("%w: durable context recorder is required", agentcontext.ErrBudgetExceeded)
	}
	plan, err := builder.PlanCompaction(contextState)
	if err != nil {
		return provider.ChatRequest{}, err
	}
	summaryRequest, err := builder.BuildSummaryRequest(plan)
	if err != nil {
		return provider.ChatRequest{}, err
	}
	response, err := a.provider.Stream(ctx, summaryRequest, provider.StreamSinkFunc(func(context.Context, provider.StreamEvent) error {
		return nil
	}))
	if err != nil {
		return provider.ChatRequest{}, fmt.Errorf("generate context summary: %w", err)
	}
	if err := validateResponse(response); err != nil || len(response.Message.ToolCalls) != 0 {
		return provider.ChatRequest{}, fmt.Errorf("generate context summary: %w", ErrInvalidResponse)
	}
	summary := strings.TrimSpace(response.Message.Content)
	if summary == "" || len([]byte(summary)) > 64<<10 {
		return provider.ChatRequest{}, errors.New("generate context summary: response must be between 1 byte and 64 KiB")
	}
	if err := agentcontext.ValidateSummary(summary); err != nil {
		return provider.ChatRequest{}, fmt.Errorf("generate context summary: %w", err)
	}
	compactedRequest, err := builder.Build(agentcontext.State{
		Messages: plan.Retained, SourceMessages: plan.RetainedSource,
		Todos: contextState.Todos, Summary: summary,
	})
	if err != nil {
		return provider.ChatRequest{}, fmt.Errorf("validate generated context summary: %w", err)
	}
	generatorModel := strings.TrimSpace(response.Model)
	if generatorModel == "" {
		generatorModel = model
	}
	receipt, err := a.contextRecorder.RecordCompaction(context.WithoutCancel(ctx), agentcontext.CompactionRecord{
		Summary: summary, RetainedTailMessages: plan.RetainedTailMessages,
		GeneratorModel: generatorModel, GeneratorVersion: agentcontext.SummaryProtocolVersion,
		EstimatedBefore: plan.EstimatedBefore, EstimatedAfterUpperBound: plan.EstimatedAfterUpperBound,
	})
	if err != nil {
		return provider.ChatRequest{}, fmt.Errorf("persist private context summary: %w", err)
	}
	emitContext := context.WithoutCancel(ctx)
	if _, err := emitter.Emit(emitContext, events.KindContextCompacted, events.ContextCompacted{
		SourceStart: receipt.SourceStart, SourceEnd: receipt.SourceEnd,
		EstimatedBefore: plan.EstimatedBefore, EstimatedAfterUpperBound: plan.EstimatedAfterUpperBound,
		GeneratorModel:   generatorModel,
		GeneratorVersion: agentcontext.SummaryProtocolVersion,
	}); err != nil {
		return provider.ChatRequest{}, err
	}
	if response.Usage != (provider.Usage{}) {
		if _, err := emitter.Emit(emitContext, events.KindUsageUpdated, events.UsageUpdated{
			InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens,
			CacheReadTokens: response.Usage.CacheReadTokens,
		}); err != nil {
			return provider.ChatRequest{}, err
		}
	}
	state.applyCompaction(plan.Retained, plan.RetainedSource, summary)
	return compactedRequest, nil
}

func validateRunRequest(request RunRequest) error {
	switch {
	case strings.TrimSpace(request.RunID) == "":
		return errors.New("agent: run_id is required")
	case strings.TrimSpace(request.Task) == "":
		return errors.New("agent: task is required")
	case strings.TrimSpace(request.Model) == "":
		return errors.New("agent: model is required")
	case request.MaxTurns < 1 || request.MaxTurns > 256:
		return errors.New("agent: max turns must be between 1 and 256")
	default:
		messages := append(cloneMessages(request.History), provider.Message{Role: provider.RoleUser, Content: request.Task})
		if err := (provider.ChatRequest{Model: request.Model, Messages: messages}).Validate(); err != nil {
			return fmt.Errorf("agent: invalid recovery history: %w", err)
		}
		if request.Recovery != nil {
			if err := validateRecoveryAction(*request.Recovery); err != nil {
				return err
			}
		}
		return nil
	}
}

func validateRecoveryAction(action RecoveryAction) error {
	if strings.TrimSpace(action.SourceRunID) == "" {
		return errors.New("agent: recovery source run id is required")
	}
	if action.Kind != RecoveryReapprove && action.Kind != RecoveryRetryRead &&
		action.Kind != RecoveryRetryWrite && action.Kind != RecoveryVerifyWrite {
		return fmt.Errorf("agent: unsupported recovery action %q", action.Kind)
	}
	if err := validateResponse(&provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{action.Call}}}); err != nil {
		return fmt.Errorf("agent: invalid recovery tool call: %w", err)
	}
	return nil
}

func (a *Agent) recordContext(ctx context.Context, message provider.Message, complete bool) error {
	if a.contextRecorder == nil {
		return nil
	}
	if err := a.contextRecorder.RecordMessage(ctx, message, complete); err != nil {
		return fmt.Errorf("persist private %s context: %w", message.Role, err)
	}
	return nil
}

func validateResponse(response *provider.ChatResponse) error {
	if response == nil || response.Message.Role != provider.RoleAssistant {
		return ErrInvalidResponse
	}
	for _, call := range response.Message.ToolCalls {
		if call.ID == "" || call.Name == "" || call.Type != "function" || len(call.Arguments) == 0 || !json.Valid(call.Arguments) {
			return ErrInvalidResponse
		}
	}
	return nil
}

type toolOutcome struct {
	message        string
	resumeMessage  string
	resumeComplete bool
	failed         bool
	failureKey     string
	denied         bool
	fatal          error
}

func recoveryOutcome(outcome toolOutcome) string {
	if outcome.failed || outcome.denied {
		return "failed"
	}
	return "completed"
}

func (a *Agent) executeRecovered(ctx context.Context, state *RunState, emitter *events.Emitter, authorizer *policy.Authorizer, recovery RecoveryAction) toolOutcome {
	call := recovery.Call
	if recovery.Kind == RecoveryVerifyWrite {
		if _, err := emitter.Emit(ctx, events.KindToolProposed, events.ToolProposed{
			CallID: call.ID, Tool: call.Name, Summary: "Patch Journal 已核验上次写入", Effects: []string{string(tools.EffectWrite)},
		}); err != nil {
			return toolOutcome{fatal: err}
		}
		if _, err := emitter.Emit(ctx, events.KindToolCompleted, events.ToolCompleted{
			CallID: call.ID, Tool: call.Name, Success: true, Summary: "已确认上次写入完成，未重复执行",
		}); err != nil {
			return toolOutcome{fatal: err}
		}
		message := toolMessage(true, &tools.ToolResult{Output: "Patch Journal 已确认上次写入完成；本次恢复没有重复修改文件。"}, "", nil)
		resumeMessage, complete := resumableToolMessage(message, []tools.Effect{tools.EffectWrite}, true, "")
		return toolOutcome{message: message, resumeMessage: resumeMessage, resumeComplete: complete}
	}
	tool, ok := a.registry.Lookup(call.Name)
	if !ok {
		if _, err := emitter.Emit(context.WithoutCancel(ctx), events.KindToolProposed, events.ToolProposed{
			CallID: call.ID, Tool: call.Name, Summary: "恢复时发现未知工具",
		}); err != nil {
			return toolOutcome{fatal: err}
		}
		return a.failedTool(ctx, emitter, call, "unknown_tool", fmt.Errorf("unknown tool %q", call.Name))
	}
	environment := append([]string(nil), a.environment()...)
	prepared, err := tool.Prepare(ctx, call.Arguments, tools.PrepareEnv{
		CallID: call.ID, Guard: a.guard, Now: a.now, Environment: environment,
		AllowedEnvironment: a.allowedEnvironment,
	})
	if err != nil {
		spec := tool.Spec()
		if _, emitErr := emitter.Emit(context.WithoutCancel(ctx), events.KindToolProposed, events.ToolProposed{
			CallID: call.ID, Tool: call.Name, Summary: "恢复时重新准备失败", Effects: effectStrings(spec.Effects),
		}); emitErr != nil {
			return toolOutcome{fatal: emitErr}
		}
		return a.failedTool(ctx, emitter, call, "recovery_prepare_failed", err)
	}
	if _, err := emitter.Emit(ctx, events.KindToolProposed, events.ToolProposed{
		CallID: call.ID, Tool: call.Name, Summary: "恢复：" + prepared.Preview.Title, Effects: effectStrings(prepared.Effects),
	}); err != nil {
		return toolOutcome{fatal: err}
	}
	prompt := "恢复前请确认当前预览；旧审批和旧 Capability 已失效。"
	switch recovery.Kind {
	case RecoveryRetryRead:
		prompt = "上次只读工具已开始但结果未知；请确认按当前状态重新执行。"
	case RecoveryRetryWrite:
		prompt = "Patch Journal 已确认上次写入未发生；请按当前文件状态重新检查预览并确认是否执行。"
	}
	capability, err := authorizer.Reauthorize(ctx, state.RunID, a.guard.Root(), prepared, prompt)
	if err != nil {
		category := "recovery_authorization_failed"
		if errors.Is(err, policy.ErrDenied) {
			category = "denied"
		} else if errors.Is(err, policy.ErrReprepare) {
			category = "approval_edited"
		}
		outcome := a.failedTool(ctx, emitter, call, category, err)
		outcome.denied = category == "denied"
		return outcome
	}
	if _, err := emitter.Emit(ctx, events.KindToolStarted, events.ToolStarted{CallID: call.ID, Tool: call.Name}); err != nil {
		return toolOutcome{fatal: err}
	}
	started := a.now()
	result, runErr := tool.Execute(ctx, prepared, capability, tools.ExecEnv{
		RunID: state.RunID, Guard: a.guard, CapabilityVerifier: authorizer.Verifier(),
		Now: a.now, Environment: environment, TodoWriter: todoWriter{state: state, emitter: emitter},
		MutationJournal: a.mutationJournal,
	})
	duration := a.now().Sub(started)
	if runErr != nil {
		if _, err := emitter.Emit(context.WithoutCancel(ctx), events.KindToolCompleted, events.ToolCompleted{
			CallID: call.ID, Tool: call.Name, Success: false, Summary: "恢复执行失败", DurationMS: duration.Milliseconds(),
		}); err != nil {
			return toolOutcome{fatal: err}
		}
		message := toolMessage(false, result, "recovery_execute_failed", runErr)
		resumeMessage, complete := resumableToolMessage(message, prepared.Effects, false, "recovery_execute_failed")
		return toolOutcome{message: message, resumeMessage: resumeMessage, resumeComplete: complete, failed: true, failureKey: "recovery_execute_failed:" + runErr.Error()}
	}
	if _, err := emitter.Emit(ctx, events.KindToolCompleted, events.ToolCompleted{
		CallID: call.ID, Tool: call.Name, Success: true, Summary: "恢复执行完成", DurationMS: duration.Milliseconds(),
	}); err != nil {
		return toolOutcome{fatal: err}
	}
	message := toolMessage(true, result, "", nil)
	resumeMessage, complete := resumableToolMessage(message, prepared.Effects, true, "")
	return toolOutcome{message: message, resumeMessage: resumeMessage, resumeComplete: complete}
}

func (a *Agent) executeOne(ctx context.Context, state *RunState, emitter *events.Emitter, authorizer *policy.Authorizer, call provider.ToolCall) toolOutcome {
	tool, ok := a.registry.Lookup(call.Name)
	if !ok {
		if _, err := emitter.Emit(context.WithoutCancel(ctx), events.KindToolProposed, events.ToolProposed{
			CallID: call.ID, Tool: call.Name, Summary: "未知工具",
		}); err != nil {
			return toolOutcome{fatal: err}
		}
		return a.failedTool(ctx, emitter, call, "unknown_tool", fmt.Errorf("unknown tool %q", call.Name))
	}
	environment := append([]string(nil), a.environment()...)
	prepared, err := tool.Prepare(ctx, call.Arguments, tools.PrepareEnv{
		CallID: call.ID, Guard: a.guard, Now: a.now, Environment: environment,
		AllowedEnvironment: a.allowedEnvironment,
	})
	if err != nil {
		spec := tool.Spec()
		if _, emitErr := emitter.Emit(context.WithoutCancel(ctx), events.KindToolProposed, events.ToolProposed{
			CallID: call.ID, Tool: call.Name, Summary: "参数准备失败", Effects: effectStrings(spec.Effects),
		}); emitErr != nil {
			return toolOutcome{fatal: emitErr}
		}
		return a.failedTool(ctx, emitter, call, "prepare_failed", err)
	}
	if _, err := emitter.Emit(ctx, events.KindToolProposed, events.ToolProposed{
		CallID: call.ID, Tool: call.Name, Summary: prepared.Preview.Title,
		Effects: effectStrings(prepared.Effects),
	}); err != nil {
		return toolOutcome{fatal: err}
	}
	capability, err := authorizer.Authorize(ctx, state.RunID, a.guard.Root(), prepared)
	if err != nil {
		category := "authorization_failed"
		if errors.Is(err, policy.ErrDenied) {
			category = "denied"
		} else if errors.Is(err, policy.ErrReprepare) {
			category = "approval_edited"
		}
		outcome := a.failedTool(ctx, emitter, call, category, err)
		outcome.denied = category == "denied"
		return outcome
	}
	if _, err := emitter.Emit(ctx, events.KindToolStarted, events.ToolStarted{CallID: call.ID, Tool: call.Name}); err != nil {
		return toolOutcome{fatal: err}
	}
	started := a.now()
	result, runErr := tool.Execute(ctx, prepared, capability, tools.ExecEnv{
		RunID: state.RunID, Guard: a.guard, CapabilityVerifier: authorizer.Verifier(),
		Now: a.now, Environment: environment,
		TodoWriter:      todoWriter{state: state, emitter: emitter},
		MutationJournal: a.mutationJournal,
	})
	duration := a.now().Sub(started)
	if runErr != nil {
		if _, err := emitter.Emit(context.WithoutCancel(ctx), events.KindToolCompleted, events.ToolCompleted{
			CallID: call.ID, Tool: call.Name, Success: false, Summary: "执行失败", DurationMS: duration.Milliseconds(),
		}); err != nil {
			return toolOutcome{fatal: err}
		}
		message := toolMessage(false, result, "execute_failed", runErr)
		resumeMessage, complete := resumableToolMessage(message, prepared.Effects, false, "execute_failed")
		return toolOutcome{
			message: message, resumeMessage: resumeMessage, resumeComplete: complete, failed: true,
			failureKey: "execute_failed:" + runErr.Error(),
		}
	}
	if _, err := emitter.Emit(ctx, events.KindToolCompleted, events.ToolCompleted{
		CallID: call.ID, Tool: call.Name, Success: true, Summary: "完成", DurationMS: duration.Milliseconds(),
	}); err != nil {
		return toolOutcome{fatal: err}
	}
	message := toolMessage(true, result, "", nil)
	resumeMessage, complete := resumableToolMessage(message, prepared.Effects, true, "")
	return toolOutcome{message: message, resumeMessage: resumeMessage, resumeComplete: complete}
}

func (a *Agent) failedTool(ctx context.Context, emitter *events.Emitter, call provider.ToolCall, category string, cause error) toolOutcome {
	_, emitErr := emitter.Emit(context.WithoutCancel(ctx), events.KindToolCompleted, events.ToolCompleted{
		CallID: call.ID, Tool: call.Name, Success: false, Summary: category,
	})
	if emitErr != nil {
		return toolOutcome{fatal: emitErr}
	}
	message := toolMessage(false, nil, category, cause)
	return toolOutcome{
		message: message, resumeMessage: safeToolSummary(false, category), resumeComplete: false, failed: true,
		failureKey: category + ":" + cause.Error(),
	}
}

func resumableToolMessage(message string, effects []tools.Effect, success bool, category string) (string, bool) {
	for _, effect := range effects {
		if effect != tools.EffectRead && effect != tools.EffectState {
			return safeToolSummary(success, category), false
		}
	}
	return message, true
}

func safeToolSummary(success bool, category string) string {
	envelope := toolMessageEnvelope{
		Success: success, Category: category,
		Output:   "恢复摘要：原始副作用工具输出未持久化；继续前必须依据当前仓库状态重新验证。",
		Metadata: map[string]string{"recovery": "sanitized"},
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return `{"success":false,"category":"recovery_summary_encoding_failed"}`
	}
	return string(raw)
}

type toolMessageEnvelope struct {
	Success   bool              `json:"success"`
	Output    string            `json:"output,omitempty"`
	Error     string            `json:"error,omitempty"`
	Category  string            `json:"category,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

func toolMessage(success bool, result *tools.ToolResult, category string, cause error) string {
	envelope := toolMessageEnvelope{Success: success, Category: category}
	if result != nil {
		envelope.Output = result.Output
		envelope.Truncated = result.Truncated
		envelope.Metadata = result.Metadata
	}
	if cause != nil {
		envelope.Error = cause.Error()
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return `{"success":false,"category":"result_encoding_failed"}`
	}
	return string(raw)
}

func canonicalCallKey(call provider.ToolCall) (string, error) {
	canonical, err := tools.Canonicalize(call.Arguments)
	if err != nil {
		return "", fmt.Errorf("%w: invalid tool arguments: %v", ErrInvalidResponse, err)
	}
	return call.Name + ":" + tools.ComputeDigest(call.Name, canonical), nil
}

type repetitionTracker struct {
	lastCall     string
	callCount    int
	lastFailure  string
	failureCount int
}

func (t *repetitionTracker) observeCall(key string) int {
	if key == t.lastCall {
		t.callCount++
	} else {
		t.lastCall, t.callCount = key, 1
	}
	return t.callCount
}

func (t *repetitionTracker) observeFailure(key string) int {
	if key == t.lastFailure {
		t.failureCount++
	} else {
		t.lastFailure, t.failureCount = key, 1
	}
	return t.failureCount
}

func (t *repetitionTracker) resetFailure() {
	t.lastFailure, t.failureCount = "", 0
}

type todoWriter struct {
	state   *RunState
	emitter *events.Emitter
}

func (writer todoWriter) ReplaceTodos(ctx context.Context, todos []tools.Todo) error {
	eventTodos := make([]events.Todo, len(todos))
	for index, todo := range todos {
		eventTodos[index] = events.Todo{ID: todo.ID, Content: todo.Content, Status: string(todo.Status)}
	}
	if _, err := writer.emitter.Emit(ctx, events.KindTodoUpdated, events.TodoUpdated{Todos: eventTodos}); err != nil {
		return err
	}
	writer.state.replaceTodos(todos)
	return nil
}

type streamObserver struct {
	emitter *events.Emitter
	state   *RunState
}

func (observer *streamObserver) OnEvent(ctx context.Context, event provider.StreamEvent) error {
	switch event.Kind {
	case provider.StreamTextDelta:
		if event.Text == "" {
			return nil
		}
		_, err := observer.emitter.Emit(ctx, events.KindMessageDelta, events.MessageDelta{Text: event.Text})
		return err
	case provider.StreamUsage:
		if event.Usage == nil || !observer.state.setUsage(*event.Usage) {
			return nil
		}
		return observer.emitUsage(ctx, *event.Usage)
	case provider.StreamRetry:
		if event.Retry == nil {
			return nil
		}
		_, err := observer.emitter.Emit(ctx, events.KindWarning, events.Warning{
			Code:    "provider_retry",
			Message: fmt.Sprintf("Provider 重试 %d/%d，等待 %s", event.Retry.Attempt, event.Retry.MaxAttempts, event.Retry.Delay),
		})
		return err
	default:
		return nil
	}
}

func (observer *streamObserver) emitUsage(ctx context.Context, usage provider.Usage) error {
	_, err := observer.emitter.Emit(ctx, events.KindUsageUpdated, events.UsageUpdated{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheReadTokens: usage.CacheReadTokens,
	})
	return err
}

func effectStrings(effects []tools.Effect) []string {
	result := make([]string, len(effects))
	for index, effect := range effects {
		result[index] = string(effect)
	}
	return result
}

func (a *Agent) finishError(ctx context.Context, emitter *events.Emitter, err error) error {
	emitContext := context.WithoutCancel(ctx)
	if errors.Is(err, context.Canceled) || provider.CategoryOf(err) == provider.ErrorCanceled {
		_, emitErr := emitter.Emit(emitContext, events.KindRunCancelled, events.RunCancelled{Reason: "user_cancelled"})
		return errors.Join(err, emitErr)
	}
	if errors.Is(err, context.DeadlineExceeded) || provider.CategoryOf(err) == provider.ErrorTimeout {
		_, emitErr := emitter.Emit(emitContext, events.KindRunCancelled, events.RunCancelled{Reason: "timeout"})
		return errors.Join(err, emitErr)
	}
	category, retryable := errorCategory(err)
	_, emitErr := emitter.Emit(emitContext, events.KindRunFailed, events.RunFailed{
		Category: category, Message: err.Error(), Retryable: retryable,
	})
	return errors.Join(err, emitErr)
}

func errorCategory(err error) (string, bool) {
	if providerErr, ok := provider.AsError(err); ok {
		return string(providerErr.Category), providerErr.Retryable
	}
	switch {
	case errors.Is(err, agentcontext.ErrBudgetExceeded):
		return "context_budget", false
	case errors.Is(err, agentcontext.ErrToolCallingDisabled):
		return "tool_calling_unsupported", false
	case errors.Is(err, ErrMaxTurns):
		return "max_turns", false
	case errors.Is(err, ErrRepeatedCall):
		return "repeated_tool_call", false
	case errors.Is(err, ErrRepeatedFailure):
		return "repeated_tool_failure", false
	case errors.Is(err, ErrInvalidResponse):
		return "provider_protocol", false
	default:
		return "runtime", false
	}
}
