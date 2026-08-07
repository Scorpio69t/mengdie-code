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
}

type RunRequest struct {
	RunID        string
	Task         string
	Model        string
	DisplayModel string
	MaxTurns     int
	Security     string
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
		Messages: []provider.Message{{Role: provider.RoleUser, Content: request.Task}},
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
	if err := ctx.Err(); err != nil {
		return state.result(""), a.finishError(ctx, emitter, err)
	}

	capabilities, err := a.provider.Capabilities(ctx, request.Model)
	if err != nil {
		return state.result(""), a.finishError(ctx, emitter, err)
	}
	builder, err := agentcontext.NewBuilder(agentcontext.Options{
		Model: request.Model, SystemPrompt: a.systemPrompt,
		MaxContextTokens: a.maxContextTokens, Capabilities: capabilities,
		Tools:        a.registry.Specs(),
		Instructions: a.instructions,
	})
	if err != nil {
		return state.result(""), a.finishError(ctx, emitter, err)
	}
	authorizer, err := policy.NewAuthorizer(policy.AuthorizerOptions{
		Engine: a.policy, Broker: a.broker, Observer: policy.EventObserver{Emitter: emitter}, Now: a.now,
	})
	if err != nil {
		return state.result(""), a.finishError(ctx, emitter, err)
	}

	tracker := repetitionTracker{}
	for state.Turn < request.MaxTurns {
		if err := ctx.Err(); err != nil {
			return state.result(""), a.finishError(ctx, emitter, err)
		}
		state.Turn++
		messages, todos := state.snapshot()
		chatRequest, err := builder.Build(agentcontext.State{Messages: messages, Todos: todos})
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
			state.appendMessage(provider.Message{
				Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Name, Content: outcome.message,
			})
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
		return nil
	}
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
	message    string
	failed     bool
	failureKey string
	denied     bool
	fatal      error
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
		TodoWriter: todoWriter{state: state, emitter: emitter},
	})
	duration := a.now().Sub(started)
	if runErr != nil {
		if _, err := emitter.Emit(context.WithoutCancel(ctx), events.KindToolCompleted, events.ToolCompleted{
			CallID: call.ID, Tool: call.Name, Success: false, Summary: "执行失败", DurationMS: duration.Milliseconds(),
		}); err != nil {
			return toolOutcome{fatal: err}
		}
		return toolOutcome{
			message: toolMessage(false, result, "execute_failed", runErr), failed: true,
			failureKey: "execute_failed:" + runErr.Error(),
		}
	}
	if _, err := emitter.Emit(ctx, events.KindToolCompleted, events.ToolCompleted{
		CallID: call.ID, Tool: call.Name, Success: true, Summary: "完成", DurationMS: duration.Milliseconds(),
	}); err != nil {
		return toolOutcome{fatal: err}
	}
	return toolOutcome{message: toolMessage(true, result, "", nil)}
}

func (a *Agent) failedTool(ctx context.Context, emitter *events.Emitter, call provider.ToolCall, category string, cause error) toolOutcome {
	_, emitErr := emitter.Emit(context.WithoutCancel(ctx), events.KindToolCompleted, events.ToolCompleted{
		CallID: call.ID, Tool: call.Name, Success: false, Summary: category,
	})
	if emitErr != nil {
		return toolOutcome{fatal: emitErr}
	}
	return toolOutcome{
		message: toolMessage(false, nil, category, cause), failed: true,
		failureKey: category + ":" + cause.Error(),
	}
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
