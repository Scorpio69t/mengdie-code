// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/session"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func (a *App) runRewind(ctx context.Context, args []string, interactive bool) int {
	if !interactive {
		if err := a.writeError("rewind 只能在交互终端执行；该操作必须查看当前 diff 并显式确认\n"); err != nil {
			return ExitRunError
		}
		return ExitPolicyDenied
	}
	flags, common := a.newCommonFlagSet("mengdie rewind")
	commandID := flags.String("command-id", "", "幂等回滚命令 ID；重复 ID 只恢复已提交状态")
	journalID := flags.String("journal-id", "", "指定 Patch Journal；默认选择该会话最近一个可回滚 Journal")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 1 {
		if err := a.writeError("用法：mengdie rewind [--journal-id ID] [--command-id ID] <session-id>\n"); err != nil {
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
	loaded, store, _, code := a.openSessionService(ctx, common)
	if code != ExitOK {
		return code
	}
	sessionID := flags.Arg(0)
	resolvedJournalID := strings.TrimSpace(*journalID)
	var err error
	if resolvedJournalID == "" {
		resolvedJournalID, err = store.ResolveRewindJournal(ctx, sessionID, loaded.ProjectRoot)
		if err != nil {
			return a.closeRewindError(store, fmt.Sprintf("没有可安全回滚的变更：%v", err), ExitPolicyDenied)
		}
	}
	resolvedCommandID := strings.TrimSpace(*commandID)
	if resolvedCommandID == "" {
		generated, generateErr := a.newRunID()
		if generateErr != nil {
			return a.closeRewindError(store, fmt.Sprintf("创建回滚命令 ID 失败：%v", generateErr), ExitRunError)
		}
		resolvedCommandID = "rewind_" + generated
	}
	begin, err := store.BeginRewindCommand(ctx, sessionID, resolvedJournalID, resolvedCommandID, loaded.ProjectRoot)
	if err != nil {
		return a.closeRewindError(store, fmt.Sprintf("登记回滚命令失败：%v", err), ExitPolicyDenied)
	}
	if begin.Existing {
		command, recoverErr := store.RecoverRewindCommand(ctx, resolvedCommandID, loaded.ProjectRoot)
		if recoverErr != nil {
			return a.closeRewindError(store, fmt.Sprintf("恢复既有回滚命令失败：%v", recoverErr), ExitToolFailure)
		}
		return a.finishExistingRewind(store, command, resolvedJournalID)
	}

	guard, err := platform.NewPathGuard(loaded.ProjectRoot)
	if err != nil {
		_ = store.FailRewindCommand(context.WithoutCancel(ctx), resolvedCommandID)
		return a.closeRewindError(store, fmt.Sprintf("初始化项目边界失败：%v", err), ExitRunError)
	}
	tool, err := tools.NewRewindFile(store, resolvedCommandID)
	if err != nil {
		_ = store.FailRewindCommand(context.WithoutCancel(ctx), resolvedCommandID)
		return a.closeRewindError(store, fmt.Sprintf("初始化回滚工具失败：%v", err), ExitRunError)
	}
	raw, err := json.Marshal(map[string]string{"session_id": sessionID, "journal_id": resolvedJournalID})
	if err != nil {
		_ = store.FailRewindCommand(context.WithoutCancel(ctx), resolvedCommandID)
		return a.closeRewindError(store, fmt.Sprintf("编码回滚目标失败：%v", err), ExitRunError)
	}
	call, err := tool.Prepare(ctx, raw, tools.PrepareEnv{CallID: "rewind-" + resolvedJournalID, Guard: guard})
	if err != nil {
		_ = store.FailRewindCommand(context.WithoutCancel(ctx), resolvedCommandID)
		return a.closeRewindError(store, fmt.Sprintf("回滚安全检查失败：%v", err), ExitPolicyDenied)
	}
	engine, err := policy.NewEngine(policy.Options{Root: loaded.ProjectRoot, Mode: policy.ModeInteractive})
	if err != nil {
		_ = store.FailRewindCommand(context.WithoutCancel(ctx), resolvedCommandID)
		return a.closeRewindError(store, fmt.Sprintf("初始化回滚策略失败：%v", err), ExitRunError)
	}
	broker, err := policy.NewTextBroker(bufio.NewReader(a.stdin), a.stdout)
	if err != nil {
		_ = store.FailRewindCommand(context.WithoutCancel(ctx), resolvedCommandID)
		return a.closeRewindError(store, fmt.Sprintf("初始化回滚审批失败：%v", err), ExitRunError)
	}
	authorizer, err := policy.NewAuthorizer(policy.AuthorizerOptions{Engine: engine, Broker: broker, Now: a.now})
	if err != nil {
		_ = store.FailRewindCommand(context.WithoutCancel(ctx), resolvedCommandID)
		return a.closeRewindError(store, fmt.Sprintf("初始化回滚授权失败：%v", err), ExitRunError)
	}
	prompt := fmt.Sprintf("高风险操作：是否将 Journal %s 的单文件变更恢复到写前状态？", resolvedJournalID)
	capability, err := authorizer.Reauthorize(ctx, resolvedCommandID, loaded.ProjectRoot, call, prompt)
	if err != nil {
		_ = store.RejectRewindCommand(context.WithoutCancel(ctx), resolvedCommandID)
		code := ExitPolicyDenied
		if errors.Is(err, context.Canceled) {
			code = ExitUserCanceled
		}
		return a.closeRewindError(store, fmt.Sprintf("回滚未获批准：%v", err), code)
	}
	result, err := tool.Execute(ctx, call, capability, tools.ExecEnv{
		RunID: resolvedCommandID, Guard: guard, CapabilityVerifier: authorizer.Verifier(), Now: a.now,
	})
	if err != nil {
		command, recoverErr := store.RecoverRewindCommand(context.WithoutCancel(ctx), resolvedCommandID, loaded.ProjectRoot)
		if recoverErr == nil && command.Status == session.CommandApplied {
			if _, outputErr := fmt.Fprintf(a.stdout, "回滚已在中断前完成：Journal %s\n", resolvedJournalID); outputErr != nil {
				return a.closeRewindError(store, fmt.Sprintf("输出回滚结果失败：%v", outputErr), ExitRunError)
			}
			return a.closeSessionStore(store)
		}
		return a.closeRewindError(store, fmt.Sprintf("执行回滚失败：%v", errors.Join(err, recoverErr)), ExitToolFailure)
	}
	if _, err := fmt.Fprintf(a.stdout, "%s\n命令：%s\nJournal：%s\n", result.Output, resolvedCommandID, resolvedJournalID); err != nil {
		return a.closeRewindError(store, fmt.Sprintf("输出回滚结果失败：%v", err), ExitRunError)
	}
	return a.closeSessionStore(store)
}

func (a *App) finishExistingRewind(store *session.SQLiteStore, command session.Command, journalID string) int {
	if command.Status == session.CommandApplied {
		if _, err := fmt.Fprintf(a.stdout, "回滚命令 %s 已完成；未重复执行。Journal：%s\n", command.ID, journalID); err != nil {
			return a.closeRewindError(store, fmt.Sprintf("输出回滚结果失败：%v", err), ExitRunError)
		}
		return a.closeSessionStore(store)
	}
	code := ExitToolFailure
	if command.Status == session.CommandRejected {
		code = ExitPolicyDenied
	}
	return a.closeRewindError(store, fmt.Sprintf("回滚命令 %s 已处于 %s；未重复执行", command.ID, command.Status), code)
}

func (a *App) closeRewindError(store *session.SQLiteStore, message string, code int) int {
	if store != nil {
		if err := store.Close(); err != nil {
			message = fmt.Sprintf("%s；关闭会话存储失败：%v", message, err)
			code = ExitRunError
		}
	}
	if err := a.writeError("%s\n", message); err != nil {
		return ExitRunError
	}
	return code
}
