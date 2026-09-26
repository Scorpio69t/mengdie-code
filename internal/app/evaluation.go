// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/agent"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

// EvaluationPolicy is a local diagnostic ceiling, intersected with Policy.
// CommandPrefixes must be selected by the operator, independently of the
// repository manifest. It cannot constrain subprocesses spawned by a command.
type EvaluationPolicy struct {
	AllowedEffects  []tools.Effect
	AllowedChanges  []string
	AllowEdit       bool
	CommandPrefixes []string
	MaxToolCalls    int
	MaxCommandCalls int
}

type EvaluationRunOptions struct {
	Workspace   string
	StateDir    string
	Profile     string
	Prompt      string
	Environment []string
	Policy      EvaluationPolicy
}

// EvaluationRunResult contains no model text, tool arguments or raw events.
type EvaluationRunResult struct {
	Status                string
	ExitCode              int
	ToolCalls             int
	CommandCalls          int
	DeniedTools           int
	ForcedCleanupCount    int
	UncertainExecuteCount int
	InputTokens           int64
	OutputTokens          int64
}

type discardEvaluationEvents struct{}

func (discardEvaluationEvents) Emit(_ context.Context, event events.Event) error {
	return event.Validate()
}

// RunEvaluation executes one task with temporary state and a bounded Policy.
// This is a diagnostic within the caller's OS account, not a sandbox.
func (a *App) RunEvaluation(ctx context.Context, options EvaluationRunOptions) (EvaluationRunResult, error) {
	if ctx == nil || strings.TrimSpace(options.Prompt) == "" || options.Policy.MaxToolCalls < 1 ||
		options.Policy.MaxCommandCalls < 0 || options.Policy.MaxCommandCalls > options.Policy.MaxToolCalls {
		return EvaluationRunResult{}, errors.New("invalid evaluation run options")
	}
	for _, effect := range options.Policy.AllowedEffects {
		switch effect {
		case tools.EffectRead, tools.EffectWrite, tools.EffectExecute, tools.EffectNetwork:
		default:
			return EvaluationRunResult{}, errors.New("invalid evaluation effect")
		}
	}
	for _, path := range options.Policy.AllowedChanges {
		if !filepath.IsLocal(filepath.FromSlash(path)) || filepath.Clean(filepath.FromSlash(path)) == "." {
			return EvaluationRunResult{}, errors.New("invalid evaluation change path")
		}
	}
	workspace, err := filepath.Abs(options.Workspace)
	if err != nil {
		return EvaluationRunResult{}, errors.New("invalid evaluation workspace")
	}
	stateDir, err := filepath.Abs(options.StateDir)
	if err != nil {
		return EvaluationRunResult{}, errors.New("invalid evaluation state directory")
	}
	if workspace == stateDir || executableInsideEvaluationWorkspace(stateDir, workspace) {
		return EvaluationRunResult{}, errors.New("evaluation state must be outside workspace")
	}
	if info, err := os.Stat(stateDir); err != nil || !info.IsDir() {
		return EvaluationRunResult{}, errors.New("evaluation state directory is unavailable")
	}
	// Load only user configuration. A repository-supplied project config must
	// never redirect the Provider endpoint or raise approval permissions.
	loaded, err := a.loadConfig(&commonFlags{cwd: stateDir, profile: options.Profile})
	if err != nil {
		return EvaluationRunResult{}, errors.New("evaluation provider configuration is invalid")
	}
	loaded.ProjectRoot = workspace
	loaded.WorkingDir = workspace
	loaded.ProjectConfigPath = ""
	loaded.ProjectConfigLoaded = false
	previousData, previousHome, previousEnvironment := a.dataDir, a.userHomeDir, a.environment
	a.dataDir = filepath.Join(stateDir, "data")
	a.userHomeDir = stateDir
	a.environment = func() []string { return append([]string(nil), options.Environment...) }
	defer func() { a.dataDir, a.userHomeDir, a.environment = previousData, previousHome, previousEnvironment }()
	runID, err := a.newRunID()
	if err != nil {
		return EvaluationRunResult{}, errors.New("cannot create evaluation run id")
	}
	var runResult agent.RunResult
	var runErr error
	completed := false
	task := options.Prompt + "\n\n本次诊断只允许修改以下精确路径：" + strings.Join(options.Policy.AllowedChanges, "、") +
		"。工具调用上限：" + fmt.Sprint(options.Policy.MaxToolCalls) + "；命令调用上限：" + fmt.Sprint(options.Policy.MaxCommandCalls) + "。"
	code := a.runAgent(ctx, loaded, runID, task, discardEvaluationEvents{}, runtimeOptions{
		Mode: policy.ModeHeadless, Security: "本地诊断 · 无 OS 沙箱", Evaluation: &options.Policy,
		ResultSink: func(result agent.RunResult, err error) { runResult, runErr, completed = result, err, true },
	})
	result := EvaluationRunResult{ExitCode: code, ToolCalls: runResult.ToolCalls, CommandCalls: runResult.CommandCalls,
		DeniedTools: runResult.DeniedTools, ForcedCleanupCount: runResult.ForcedCleanupCount,
		UncertainExecuteCount: runResult.UncertainExecuteCount,
		InputTokens:           runResult.Usage.InputTokens, OutputTokens: runResult.Usage.OutputTokens}
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		result.Status = "cancelled"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.Status = "timeout"
	case errors.Is(runErr, agent.ErrToolBudgetExceeded), errors.Is(runErr, agent.ErrCommandBudgetExceeded):
		result.Status = "budget_exhausted"
	case runResult.ForcedCleanupCount > 0 || runResult.UncertainExecuteCount > 0:
		result.Status = "indeterminate"
	case code == ExitPolicyDenied || runResult.DeniedTools > 0:
		result.Status = "policy_denied"
	case code == ExitOK && completed:
		result.Status = "completed"
	default:
		result.Status = "agent_failed"
	}
	return result, nil
}

func executableInsideEvaluationWorkspace(candidate, workspace string) bool {
	relative, err := filepath.Rel(workspace, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
