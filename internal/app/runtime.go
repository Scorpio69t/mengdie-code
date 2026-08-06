// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/agent"
	"github.com/Scorpio69t/mengdie-code/internal/config"
	agentcontext "github.com/Scorpio69t/mengdie-code/internal/context"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/project"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/provider/openaicompat"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

type providerFactory func(config.Profile, string) (provider.Provider, error)

type execRuntimeOptions struct {
	AllowEdit          bool
	AllowCommands      []string
	AllowedEnvironment []string
}

func defaultProviderFactory(profile config.Profile, apiKey string) (provider.Provider, error) {
	if profile.Provider != "openai-compatible" {
		return nil, fmt.Errorf("暂不支持 Provider %q", profile.Provider)
	}
	if strings.TrimSpace(profile.BaseURL) == "" {
		return nil, errors.New("openai-compatible Provider 需要 base_url")
	}
	return openaicompat.New(openaicompat.Config{
		BaseURL: profile.BaseURL, APIKey: apiKey, RequestTimeout: profile.RequestTimeout,
		Capabilities: provider.Capabilities{
			ToolCalling: true, MaxContextTokens: profile.MaxContextTokens,
		},
	})
}

func (a *App) runAgent(ctx context.Context, loaded config.Loaded, runID, task string, emitter *events.Emitter, options execRuntimeOptions) int {
	profile := loaded.Profile()
	if strings.TrimSpace(profile.Provider) == "" || strings.TrimSpace(profile.Model) == "" {
		return a.runtimeSetupError("Provider 未配置；请先运行 mengdie doctor 并配置 profile")
	}
	apiKey := ""
	if profile.APIKeyEnv != "" {
		value, ok := a.lookupEnv(profile.APIKeyEnv)
		if !ok || strings.TrimSpace(value) == "" {
			return a.runtimeSetupError(fmt.Sprintf("环境变量 %s 未设置", profile.APIKeyEnv))
		}
		apiKey = value
	}
	modelProvider, err := a.newProvider(profile, apiKey)
	if err != nil {
		return a.runtimeSetupError(err.Error())
	}
	guard, err := platform.NewPathGuard(loaded.ProjectRoot)
	if err != nil {
		return a.runtimeSetupError(fmt.Sprintf("初始化项目边界失败：%v", err))
	}
	registry, err := tools.NewRegistry(tools.DefaultTools()...)
	if err != nil {
		return a.runtimeSetupError(fmt.Sprintf("初始化工具失败：%v", err))
	}
	engine, err := policy.NewEngine(policy.Options{
		Root: loaded.ProjectRoot, Mode: policy.ModeHeadless,
		CLI: execCLIRules(options), Profile: commandRules("profile-command", loaded.Config.Approval.AllowCommands),
	})
	if err != nil {
		return a.runtimeSetupError(fmt.Sprintf("初始化策略失败：%v", err))
	}
	instructions, err := project.LoadAgents(project.AgentsOptions{
		UserConfigDir: filepath.Dir(loaded.UserConfigPath),
		ProjectRoot:   loaded.ProjectRoot, WorkDir: loaded.WorkingDir,
	})
	if err != nil {
		return a.runtimeSetupError(fmt.Sprintf("加载 AGENTS.md 失败：%v", err))
	}
	contextInstructions := make([]agentcontext.Instruction, len(instructions))
	for index, instruction := range instructions {
		contextInstructions[index] = agentcontext.Instruction{Source: instruction.Path, Content: instruction.Content}
	}
	runtime, err := agent.New(agent.Options{
		Provider: modelProvider, Registry: registry, Guard: guard, Policy: engine,
		Now: a.now, MaxContextTokens: profile.MaxContextTokens,
		Environment: a.environment, AllowedEnvironment: options.AllowedEnvironment,
		Instructions: contextInstructions,
	})
	if err != nil {
		return a.runtimeSetupError(fmt.Sprintf("初始化 Agent Runtime 失败：%v", err))
	}
	result, err := runtime.Run(ctx, agent.RunRequest{
		RunID: runID, Task: task, Model: profile.Model, DisplayModel: modelLabel(profile),
		MaxTurns: loaded.Config.Context.MaxTurns, Security: "受控本地执行 · 无头模式",
	}, emitter)
	if err != nil {
		return runtimeExitCode(err)
	}
	if result.DeniedTools > 0 {
		return ExitPolicyDenied
	}
	return ExitOK
}

func execCLIRules(options execRuntimeOptions) []policy.Rule {
	var rules []policy.Rule
	if options.AllowEdit {
		for _, name := range []string{"edit_file", "write_file"} {
			rules = append(rules, policy.Rule{
				Name: "allow-edit-" + name, Tool: name,
				Effects: []tools.Effect{tools.EffectWrite}, Decision: policy.DecisionAllow,
			})
		}
	}
	return append(rules, commandRules("cli-command", options.AllowCommands)...)
}

func commandRules(name string, prefixes []string) []policy.Rule {
	if len(prefixes) == 0 {
		return nil
	}
	return []policy.Rule{{
		Name: name, Tool: "shell", Effects: []tools.Effect{tools.EffectExecute},
		CommandPrefixes: append([]string(nil), prefixes...), Decision: policy.DecisionAllow,
	}}
}

func (a *App) runtimeSetupError(message string) int {
	if err := a.writeError("运行配置错误：%s\n", message); err != nil {
		return ExitRunError
	}
	return ExitInvalidInput
}

func runtimeExitCode(err error) int {
	if errors.Is(err, context.Canceled) {
		return ExitUserCanceled
	}
	if providerError, ok := provider.AsError(err); ok {
		switch providerError.Category {
		case provider.ErrorAuthentication, provider.ErrorPermission, provider.ErrorInvalidRequest, provider.ErrorProtocol:
			return ExitProviderError
		}
	}
	if errors.Is(err, agent.ErrRepeatedCall) || errors.Is(err, agent.ErrRepeatedFailure) {
		return ExitToolFailure
	}
	return ExitRunError
}

type stringListFlag struct{ values []string }

func (flag *stringListFlag) String() string { return strings.Join(flag.values, ",") }

func (flag *stringListFlag) Set(raw string) error {
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			return errors.New("列表参数不能包含空值")
		}
		flag.values = append(flag.values, value)
	}
	return nil
}

func (flag *stringListFlag) Values() []string {
	return append([]string(nil), flag.values...)
}

type commandPrefixFlag struct{ values []string }

func (flag *commandPrefixFlag) String() string { return strings.Join(flag.values, ",") }

func (flag *commandPrefixFlag) Set(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("命令前缀不能为空")
	}
	if strings.Contains(raw, ",") && !strings.ContainsAny(raw, " \t") {
		parts := strings.Split(raw, ",")
		for _, part := range parts {
			if strings.TrimSpace(part) == "" {
				return errors.New("命令前缀不能包含空 token")
			}
		}
		raw = strings.Join(parts, " ")
	}
	flag.values = append(flag.values, raw)
	return nil
}

func (flag *commandPrefixFlag) Values() []string {
	return append([]string(nil), flag.values...)
}
