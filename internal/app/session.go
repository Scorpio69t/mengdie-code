// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/config"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

func (a *App) runSession(ctx context.Context, args []string) int {
	if len(args) == 0 {
		if err := a.writeError("用法：mengdie session <list|show|delete> [选项]\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	switch args[0] {
	case "list":
		return a.runSessionList(ctx, args[1:])
	case "show":
		return a.runSessionShow(ctx, args[1:])
	case "delete":
		return a.runSessionDelete(ctx, args[1:])
	default:
		if err := a.writeError("未知 session 子命令 %q\n", args[0]); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
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
	dataDir, err := session.ResolveDataDir(session.DataDirOptions{Override: a.dataDir, ProjectRoot: loaded.ProjectRoot, LookupEnv: a.lookupEnv})
	if err != nil {
		return config.Loaded{}, nil, nil, a.runtimeStorageError(fmt.Sprintf("解析数据目录失败：%v", err))
	}
	store, err := session.OpenSQLite(ctx, session.OpenOptions{DataDir: dataDir, ProjectRoot: loaded.ProjectRoot, Now: a.now})
	if err != nil {
		return config.Loaded{}, nil, nil, a.runtimeStorageError(fmt.Sprintf("打开事件存储失败：%v", err))
	}
	service, err := session.NewService(store)
	if err != nil {
		return config.Loaded{}, nil, nil, a.closeSessionAfterError(store, fmt.Sprintf("初始化会话服务失败：%v", err))
	}
	return loaded, store, service, ExitOK
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
