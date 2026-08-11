// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/agent"
	"github.com/Scorpio69t/mengdie-code/internal/config"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/session"
	"github.com/Scorpio69t/mengdie-code/internal/tui"
	"github.com/Scorpio69t/mengdie-code/internal/ui/terminal"
)

func (a *App) runSession(ctx context.Context, args []string, interactive bool) int {
	if len(args) == 0 {
		if err := a.writeError("用法：mengdie session <list|show|resume|tui|delete> [选项]\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	switch args[0] {
	case "list":
		return a.runSessionList(ctx, args[1:])
	case "show":
		return a.runSessionShow(ctx, args[1:])
	case "resume":
		return a.runSessionResume(ctx, args[1:], interactive)
	case "tui":
		return a.runSessionTUI(ctx, args[1:], interactive)
	case "delete":
		return a.runSessionDelete(ctx, args[1:])
	default:
		if err := a.writeError("未知 session 子命令 %q\n", args[0]); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
}

func (a *App) runSessionTUI(ctx context.Context, args []string, interactive bool) int {
	if !interactive {
		if err := a.writeError("session tui 仅支持交互终端；请使用 session show --json 或 --plain 输出\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	flags, common := a.newCommonFlagSet("mengdie session tui")
	noColor := flags.Bool("no-color", false, "禁用颜色")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 1 {
		if err := a.writeError("用法：mengdie session tui [--no-color] <session-id>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	_, store, service, code := a.openSessionService(ctx, common)
	if code != ExitOK {
		return code
	}
	view, err := service.View(ctx, flags.Arg(0))
	if err != nil {
		return a.closeSessionAfterError(store, fmt.Sprintf("查看会话失败：%v", err))
	}
	if _, err := a.runTUI(tui.NewSubscribedSessionModel(view, 0, !*noColor, tuiSessionSource{service: service}), a.stdin, a.stdout); err != nil {
		return a.closeSessionAfterError(store, fmt.Sprintf("运行会话 TUI 失败：%v", err))
	}
	return a.closeSessionStore(store)
}

func (a *App) runSessionResume(ctx context.Context, args []string, interactive bool) int {
	flags, common := a.newCommonFlagSet("mengdie session resume")
	jsonOutput := flags.Bool("json", false, "输出 JSON Lines 事件；安全门禁拒绝时输出 JSON 结果")
	message := flags.String("message", session.DefaultResumeMessage, "追加到已有上下文的新指令")
	allowEdit := flags.Bool("allow-edit", false, "允许本次无头恢复修改项目文件")
	commandID := flags.String("command-id", "", "幂等恢复命令 ID；重复 ID 只回放已提交结果")
	var allowCommands commandPrefixFlag
	var allowEnvironment stringListFlag
	flags.Var(&allowCommands, "allow-command", "允许的非交互命令前缀，可重复；go,test 表示 go test")
	flags.Var(&allowEnvironment, "allow-env", "允许 shell 继承的敏感环境变量名，可重复或用逗号分隔")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 1 {
		if err := a.writeError("用法：mengdie session resume [--json] [--message 文本] <session-id>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	if strings.TrimSpace(*message) == "" {
		if err := a.writeError("--message 不能为空\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	if *commandID != "" && !validOpaqueCommandID(*commandID) {
		if err := a.writeError("--command-id 仅允许 1-128 个 ASCII 字母、数字及 . _ : -\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	loaded, store, service, code := a.openSessionService(ctx, common)
	if code != ExitOK {
		return code
	}
	matched, err := service.MatchResumeCommand(ctx, *commandID, flags.Arg(0), strings.TrimSpace(*message), loaded.ProjectRoot)
	if err != nil {
		return a.closeSessionAfterError(store, fmt.Sprintf("校验恢复命令失败：%v", err))
	}
	plan := session.ResumePlan{SessionID: flags.Arg(0), CanResume: matched}
	if !matched {
		plan, err = service.AnalyzeResume(ctx, flags.Arg(0), loaded.ProjectRoot)
		if err != nil {
			return a.closeSessionAfterError(store, fmt.Sprintf("分析会话恢复失败：%v", err))
		}
	}
	if !plan.CanResume {
		if *jsonOutput {
			if err := writeJSON(a.stdout, plan); err != nil {
				return a.closeSessionAfterError(store, fmt.Sprintf("输出恢复门禁结果失败：%v", err))
			}
		} else if _, err := fmt.Fprintf(a.stdout, "无法安全恢复会话 %s：%s\n", plan.SessionID, plan.Reason); err != nil {
			return a.closeSessionAfterError(store, fmt.Sprintf("输出恢复门禁结果失败：%v", err))
		}
		if closeCode := a.closeSessionStore(store); closeCode != ExitOK {
			return closeCode
		}
		return ExitPolicyDenied
	}
	if plan.Recovery != nil && recoveryNeedsApproval(plan.Recovery.Kind) && (!interactive || *jsonOutput) {
		plan.CanResume = false
		plan.Reason = "该会话需在交互终端重新确认当前工具预览；无头和 JSON 模式不会复用旧审批"
		if *jsonOutput {
			if err := writeJSON(a.stdout, plan); err != nil {
				return a.closeSessionAfterError(store, fmt.Sprintf("输出恢复门禁结果失败：%v", err))
			}
		} else if _, err := fmt.Fprintf(a.stdout, "无法安全恢复会话 %s：%s\n", plan.SessionID, plan.Reason); err != nil {
			return a.closeSessionAfterError(store, fmt.Sprintf("输出恢复门禁结果失败：%v", err))
		}
		if closeCode := a.closeSessionStore(store); closeCode != ExitOK {
			return closeCode
		}
		return ExitPolicyDenied
	}
	if closeCode := a.closeSessionStore(store); closeCode != ExitOK {
		return closeCode
	}
	runID, err := a.newRunID()
	if err != nil {
		if writeErr := a.writeError("创建恢复 Run 失败：%v\n", err); writeErr != nil {
			return ExitRunError
		}
		return ExitRunError
	}
	var sink events.Sink
	if *jsonOutput {
		sink, err = terminal.NewJSONRenderer(a.stdout)
	} else {
		sink, err = terminal.NewHumanRenderer(a.stderr)
	}
	if err != nil {
		if writeErr := a.writeError("初始化输出失败：%v\n", err); writeErr != nil {
			return ExitRunError
		}
		return ExitRunError
	}
	options := runtimeOptions{
		Mode: policy.ModeHeadless, Security: "受控本地执行 · 安全恢复",
		AllowEdit: *allowEdit, AllowCommands: allowCommands.Values(),
		AllowedEnvironment: allowEnvironment.Values(), CommandID: *commandID,
		SessionID: plan.SessionID, CommandKind: session.CommandKindResume,
		History: plan.History, ContextSummary: plan.ContextSummary, Todos: plan.Todos,
		ExpectedSessionSeq: plan.ExpectedSessionSeq, ExpectedContextOrdinal: plan.ExpectedContextOrdinal,
	}
	if plan.Recovery != nil {
		if recoveryNeedsApproval(plan.Recovery.Kind) {
			broker, brokerErr := policy.NewTextBroker(bufio.NewReader(a.stdin), a.stdout)
			if brokerErr != nil {
				return a.runtimeSetupError(fmt.Sprintf("初始化恢复审批输入失败：%v", brokerErr))
			}
			options.Mode, options.Broker = policy.ModeInteractive, broker
			options.Security = "受控本地执行 · 恢复前重新审批"
		} else {
			options.Security = "受控本地执行 · Journal 已核验"
		}
		options.Recovery = &agent.RecoveryAction{
			SourceRunID: plan.Recovery.SourceRunID, Call: plan.Recovery.Call, Kind: plan.Recovery.Kind,
		}
	}
	return a.runAgent(ctx, loaded, runID, strings.TrimSpace(*message), sink, options)
}

func recoveryNeedsApproval(kind string) bool {
	return kind != session.RecoveryVerifyWrite
}

func (a *App) runSessionList(ctx context.Context, args []string) int {
	flags, common := a.newCommonFlagSet("mengdie session list")
	jsonOutput := flags.Bool("json", false, "输出 JSON")
	allProjects := flags.Bool("all", false, "列出所有项目的会话")
	limit := flags.Int("limit", 100, "最大返回条数（1-1000）")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		if err := a.writeError("mengdie session list 不接受位置参数\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	loaded, store, service, code := a.openSessionService(ctx, common)
	if code != ExitOK {
		return code
	}
	items, err := service.List(ctx, session.ListOptions{ProjectRoot: loaded.ProjectRoot, AllProjects: *allProjects, Limit: *limit})
	if err != nil {
		return a.closeSessionAfterError(store, fmt.Sprintf("列出会话失败：%v", err))
	}
	if *jsonOutput {
		if err := writeJSON(a.stdout, items); err != nil {
			return a.closeSessionAfterError(store, fmt.Sprintf("输出会话列表失败：%v", err))
		}
	} else if len(items) == 0 {
		if _, err := fmt.Fprintln(a.stdout, "暂无会话。"); err != nil {
			return a.closeSessionAfterError(store, fmt.Sprintf("输出会话列表失败：%v", err))
		}
	} else {
		for _, item := range items {
			if _, err := fmt.Fprintf(a.stdout, "%s\t%s\t%d 条事实\t%s\n", item.ID, item.Status, item.LastSeq, item.UpdatedAt.Local().Format("2006-01-02 15:04:05")); err != nil {
				return a.closeSessionAfterError(store, fmt.Sprintf("输出会话列表失败：%v", err))
			}
		}
	}
	return a.closeSessionStore(store)
}

func (a *App) runSessionShow(ctx context.Context, args []string) int {
	flags, common := a.newCommonFlagSet("mengdie session show")
	jsonOutput := flags.Bool("json", false, "输出 JSON")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 1 {
		if err := a.writeError("用法：mengdie session show [--json] <session-id>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	_, store, service, code := a.openSessionService(ctx, common)
	if code != ExitOK {
		return code
	}
	view, err := service.View(ctx, flags.Arg(0))
	if err != nil {
		return a.closeSessionAfterError(store, fmt.Sprintf("查看会话失败：%v", err))
	}
	if *jsonOutput {
		if err := writeJSON(a.stdout, view); err != nil {
			return a.closeSessionAfterError(store, fmt.Sprintf("输出会话失败：%v", err))
		}
	} else if err := a.writeHumanSession(view); err != nil {
		return a.closeSessionAfterError(store, fmt.Sprintf("输出会话失败：%v", err))
	}
	return a.closeSessionStore(store)
}

func (a *App) runSessionDelete(ctx context.Context, args []string) int {
	flags, common := a.newCommonFlagSet("mengdie session delete")
	jsonOutput := flags.Bool("json", false, "输出 JSON")
	yes := flags.Bool("yes", false, "确认永久删除本地会话")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 1 {
		if err := a.writeError("用法：mengdie session delete --yes [--json] <session-id>\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	if !*yes {
		if err := a.writeError("删除会话会移除本地命令、事件和快照；确认后请追加 --yes\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	_, store, service, code := a.openSessionService(ctx, common)
	if code != ExitOK {
		return code
	}
	if err := service.Delete(ctx, flags.Arg(0)); err != nil {
		return a.closeSessionAfterError(store, fmt.Sprintf("删除会话失败：%v", err))
	}
	if *jsonOutput {
		if err := writeJSON(a.stdout, struct {
			ID      string `json:"id"`
			Deleted bool   `json:"deleted"`
		}{ID: flags.Arg(0), Deleted: true}); err != nil {
			return a.closeSessionAfterError(store, fmt.Sprintf("输出删除结果失败：%v", err))
		}
	} else if _, err := fmt.Fprintf(a.stdout, "已删除会话 %s。\n", flags.Arg(0)); err != nil {
		return a.closeSessionAfterError(store, fmt.Sprintf("输出删除结果失败：%v", err))
	}
	return a.closeSessionStore(store)
}

func (a *App) openSessionService(ctx context.Context, common *commonFlags) (config.Loaded, *session.SQLiteStore, *session.Service, int) {
	loaded, err := a.loadConfig(common)
	if err != nil {
		if writeErr := a.writeError("配置错误：%v\n", err); writeErr != nil {
			return config.Loaded{}, nil, nil, ExitRunError
		}
		return config.Loaded{}, nil, nil, ExitInvalidInput
	}
	store, service, code := a.openSessionServiceForLoaded(ctx, loaded)
	return loaded, store, service, code
}

func (a *App) openSessionServiceForLoaded(ctx context.Context, loaded config.Loaded) (*session.SQLiteStore, *session.Service, int) {
	dataDir, err := session.ResolveDataDir(session.DataDirOptions{Override: a.dataDir, ProjectRoot: loaded.ProjectRoot, LookupEnv: a.lookupEnv})
	if err != nil {
		return nil, nil, a.runtimeStorageError(fmt.Sprintf("解析数据目录失败：%v", err))
	}
	store, err := session.OpenSQLite(ctx, session.OpenOptions{DataDir: dataDir, ProjectRoot: loaded.ProjectRoot, Now: a.now})
	if err != nil {
		return nil, nil, a.runtimeStorageError(fmt.Sprintf("打开事件存储失败：%v", err))
	}
	service, err := session.NewService(store, session.WithPublicFactBus(a.factBus))
	if err != nil {
		return nil, nil, a.closeSessionAfterError(store, fmt.Sprintf("初始化会话服务失败：%v", err))
	}
	return store, service, ExitOK
}

func (a *App) writeHumanSession(view session.SessionView) error {
	if _, err := fmt.Fprintf(a.stdout, "会话：%s\n状态：%s\n项目：%s\n事实：%d\n", view.ID, view.Status, view.ProjectRoot, view.LastSeq); err != nil {
		return err
	}
	if len(view.Messages) > 0 {
		if _, err := fmt.Fprintln(a.stdout, "\n消息："); err != nil {
			return err
		}
		for _, message := range view.Messages {
			if _, err := fmt.Fprintf(a.stdout, "- %s\n", strings.TrimSpace(message.Text)); err != nil {
				return err
			}
		}
	}
	if len(view.Tools) > 0 {
		if _, err := fmt.Fprintln(a.stdout, "\n工具："); err != nil {
			return err
		}
		for _, tool := range view.Tools {
			if _, err := fmt.Fprintf(a.stdout, "- %s (%s)\n", tool.Tool, tool.Phase); err != nil {
				return err
			}
		}
	}
	if len(view.Recoveries) > 0 {
		if _, err := fmt.Fprintln(a.stdout, "\n恢复："); err != nil {
			return err
		}
		for _, recovery := range view.Recoveries {
			if _, err := fmt.Fprintf(a.stdout, "- %s/%s：%s（%s）\n", recovery.SourceRunID, recovery.CallID, recovery.Action, recovery.Outcome); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeJSON(writer interface{ Write([]byte) (int, error) }, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func (a *App) closeSessionStore(store *session.SQLiteStore) int {
	if err := store.Close(); err != nil {
		return a.runtimeStorageError(fmt.Sprintf("关闭会话存储失败：%v", err))
	}
	return ExitOK
}

func (a *App) closeSessionAfterError(store *session.SQLiteStore, message string) int {
	if store != nil {
		if err := store.Close(); err != nil {
			message = fmt.Sprintf("%s；关闭会话存储失败：%v", message, err)
		}
	}
	return a.runtimeStorageError(message)
}
