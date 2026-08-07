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
	"github.com/Scorpio69t/mengdie-code/internal/session"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

type providerFactory func(config.Profile, string) (provider.Provider, error)

type runtimeOptions struct {
	Mode               policy.Mode
	Broker             policy.Broker
	Security           string
	AllowEdit          bool
	AllowCommands      []string
	AllowedEnvironment []string
	CommandID          string
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

func (a *App) runAgent(ctx context.Context, loaded config.Loaded, runID, task string, sink events.Sink, options runtimeOptions) int {
	profile := loaded.Profile()
	if strings.TrimSpace(profile.Provider) == "" || strings.TrimSpace(profile.Model) == "" {
		return a.runtimeSetupError("Provider 未配置；请先运行 mengdie doctor 并配置 profile")
	}
	commandPayload, err := session.TaskCommandPayload(task)
	if err != nil {
		return a.runtimeSetupError(fmt.Sprintf("创建命令载荷失败：%v", err))
	}
	commandID := strings.TrimSpace(options.CommandID)
	if commandID == "" {
		commandID = "cmd_" + runID
	}
	sessionID := "ses_" + runID
	dataDir, err := session.ResolveDataDir(session.DataDirOptions{
		Override: a.dataDir, ProjectRoot: loaded.ProjectRoot, LookupEnv: a.lookupEnv,
	})
	if err != nil {
		return a.runtimeStorageError(fmt.Sprintf("解析数据目录失败：%v", err))
	}
	store, err := session.OpenSQLite(ctx, session.OpenOptions{
		DataDir: dataDir, ProjectRoot: loaded.ProjectRoot, Now: a.now,
	})
	if err != nil {
		return a.runtimeStorageError(fmt.Sprintf("打开事件存储失败：%v", err))
	}
	begin, err := store.BeginCommandRun(ctx, session.CommandRunMetadata{
		SessionID: sessionID, CommandID: commandID, CommandKind: session.CommandKindExec,
		CommandPayload: commandPayload, RunID: runID, ProjectRoot: loaded.ProjectRoot,
		Provider: profile.Provider, Model: profile.Model, StartedAt: a.now(),
	})
	if err != nil {
		closeErr := store.Close()
		if closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return a.runtimeStorageError(fmt.Sprintf("登记命令失败：%v", err))
	}
	if begin.Existing {
		return a.replayExistingCommand(ctx, store, begin, sink)
	}
	rejectSetup := func(message string) int {
		rejectErr := store.RejectUnstartedCommand(context.WithoutCancel(ctx), commandID)
		closeErr := store.Close()
		if rejectErr != nil || closeErr != nil {
			joined := errors.Join(rejectErr, closeErr)
			return a.runtimeStorageError(fmt.Sprintf("%s；关闭未启动命令失败：%v", message, joined))
		}
		return a.runtimeSetupError(message)
	}
	apiKey := ""
	if profile.APIKeyEnv != "" {
		value, ok := a.lookupEnv(profile.APIKeyEnv)
		if !ok || strings.TrimSpace(value) == "" {
			return rejectSetup(fmt.Sprintf("环境变量 %s 未设置", profile.APIKeyEnv))
		}
		apiKey = value
	}
	modelProvider, err := a.newProvider(profile, apiKey)
	if err != nil {
		return rejectSetup(err.Error())
	}
	guard, err := platform.NewPathGuard(loaded.ProjectRoot)
	if err != nil {
		return rejectSetup(fmt.Sprintf("初始化项目边界失败：%v", err))
	}
	registry, err := tools.NewRegistry(tools.DefaultTools()...)
	if err != nil {
		return rejectSetup(fmt.Sprintf("初始化工具失败：%v", err))
	}
	engine, err := policy.NewEngine(policy.Options{
		Root: loaded.ProjectRoot, Mode: options.Mode,
		CLI: runtimeCLIRules(options), Profile: commandRules("profile-command", loaded.Config.Approval.AllowCommands),
	})
	if err != nil {
		return rejectSetup(fmt.Sprintf("初始化策略失败：%v", err))
	}
	instructions, err := project.LoadAgents(project.AgentsOptions{
		UserConfigDir: filepath.Dir(loaded.UserConfigPath),
		ProjectRoot:   loaded.ProjectRoot, WorkDir: loaded.WorkingDir,
	})
	if err != nil {
		return rejectSetup(fmt.Sprintf("加载 AGENTS.md 失败：%v", err))
	}
	contextInstructions := make([]agentcontext.Instruction, len(instructions))
	for index, instruction := range instructions {
		contextInstructions[index] = agentcontext.Instruction{Source: instruction.Path, Content: instruction.Content}
	}
	runtime, err := agent.New(agent.Options{
		Provider: modelProvider, Registry: registry, Guard: guard, Policy: engine, Broker: options.Broker,
		Now: a.now, MaxContextTokens: profile.MaxContextTokens,
		Environment: a.environment, AllowedEnvironment: options.AllowedEnvironment,
		Instructions: contextInstructions,
	})
	if err != nil {
		return rejectSetup(fmt.Sprintf("初始化 Agent Runtime 失败：%v", err))
	}
	durableSink, err := session.NewCommandEventSink(sessionID, commandID, 0, store, sink)
	if err != nil {
		return rejectSetup(fmt.Sprintf("初始化持久事件流失败：%v", err))
	}
	emitter, err := events.NewEmitter(runID, durableSink, a.now)
	if err != nil {
		return rejectSetup(fmt.Sprintf("初始化事件流失败：%v", err))
	}
	result, err := runtime.Run(ctx, agent.RunRequest{
		RunID: runID, Task: task, Model: profile.Model, DisplayModel: modelLabel(profile),
		MaxTurns: loaded.Config.Context.MaxTurns, Security: options.Security,
	}, emitter)
	service, serviceErr := session.NewService(store)
	if serviceErr == nil {
		if snapshotErr := service.RefreshSnapshot(context.WithoutCancel(ctx), sessionID); snapshotErr != nil {
			if writeErr := a.writeError("会话快照更新失败（事件事实已保留）：%v\n", snapshotErr); writeErr != nil {
				serviceErr = errors.Join(snapshotErr, writeErr)
			}
		}
	}
	closeErr := store.Close()
	if serviceErr != nil {
		if err != nil {
			err = errors.Join(err, serviceErr)
		} else {
			err = serviceErr
		}
	}
	if err != nil {
		if closeErr != nil {
			if writeErr := a.writeError("关闭会话存储失败：%v\n", closeErr); writeErr != nil {
				return ExitRunError
			}
		}
		return runtimeExitCode(err)
	}
	if closeErr != nil {
		return a.runtimeStorageError(fmt.Sprintf("关闭会话存储失败：%v", closeErr))
	}
	if result.DeniedTools > 0 {
		return ExitPolicyDenied
	}
	return ExitOK
}

func (a *App) replayExistingCommand(ctx context.Context, store *session.SQLiteStore, begin session.BeginCommandResult, sink events.Sink) int {
	afterSeq := uint64(0)
	var terminal events.Event
	for {
		records, err := store.Load(ctx, begin.Command.SessionID, afterSeq, 1000)
		if err != nil {
			if closeErr := store.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
			return a.runtimeStorageError(fmt.Sprintf("回放既有命令失败：%v", err))
		}
		for _, record := range records {
			if record.CommandID != begin.Command.ID || record.Visibility != session.VisibilityPublic {
				continue
			}
			event := events.Event{RunID: record.RunID, Seq: record.RunSeq, Version: record.SchemaVersion, Time: record.Time, Kind: events.Kind(record.Kind), Payload: record.Payload}
			if event.Kind == events.KindRunCompleted || event.Kind == events.KindRunFailed || event.Kind == events.KindRunCancelled {
				terminal = event
			}
			if err := sink.Emit(ctx, event); err != nil {
				if closeErr := store.Close(); closeErr != nil {
					err = errors.Join(err, closeErr)
				}
				return a.runtimeStorageError(fmt.Sprintf("输出既有命令失败：%v", err))
			}
		}
		if len(records) < 1000 {
			break
		}
		afterSeq = records[len(records)-1].SessionSeq
	}
	if err := store.Close(); err != nil {
		return a.runtimeStorageError(fmt.Sprintf("关闭会话存储失败：%v", err))
	}
	switch begin.Command.Status {
	case session.CommandApplied:
		return ExitOK
	case session.CommandFailed:
		return replayedFailureExit(terminal)
	case session.CommandRejected:
		if begin.Command.ResultSeq > 0 {
			return ExitPolicyDenied
		}
		if err := a.writeError("命令 %s 已在运行前被拒绝；请使用新的 --command-id 重试\n", begin.Command.ID); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	case session.CommandInterrupted:
		if begin.Command.ResultSeq > 0 {
			return replayedFailureExit(terminal)
		}
		if err := a.writeError("命令 %s 状态为 interrupted；P2-03A 不会自动续跑，避免重复副作用\n", begin.Command.ID); err != nil {
			return ExitRunError
		}
		return ExitRunError
	default:
		if err := a.writeError("命令 %s 状态为 %s；P2-03A 不会自动续跑，避免重复副作用\n", begin.Command.ID, begin.Command.Status); err != nil {
			return ExitRunError
		}
		return ExitRunError
	}
}

func replayedFailureExit(terminal events.Event) int {
	switch terminal.Kind {
	case events.KindRunCancelled:
		payload, err := events.DecodePayload[events.RunCancelled](terminal)
		if err == nil && payload.Reason == "user_cancelled" {
			return ExitUserCanceled
		}
	case events.KindRunFailed:
		payload, err := events.DecodePayload[events.RunFailed](terminal)
		if err != nil {
			return ExitRunError
		}
		switch payload.Category {
		case "authentication", "permission", "invalid_request", "protocol", "provider_protocol":
			return ExitProviderError
		case "repeated_tool_call", "repeated_tool_failure":
			return ExitToolFailure
		}
	}
	return ExitRunError
}

func runtimeCLIRules(options runtimeOptions) []policy.Rule {
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

func (a *App) runtimeStorageError(message string) int {
	if err := a.writeError("会话存储错误：%s\n", message); err != nil {
		return ExitRunError
	}
	return ExitRunError
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
