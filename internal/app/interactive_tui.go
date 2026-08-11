// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Scorpio69t/mengdie-code/internal/brand"
	"github.com/Scorpio69t/mengdie-code/internal/config"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/tui"
)

func (a *App) runInteractiveTUI(ctx context.Context, loaded config.Loaded, color bool) int {
	store, service, code := a.openSessionServiceForLoaded(ctx, loaded)
	if code != ExitOK {
		return code
	}
	broker := tui.NewApprovalBroker()
	runner := &interactiveTaskRunner{ctx: ctx, app: a, loaded: loaded, broker: broker}
	model := tui.NewInteractiveModel(brand.Info{
		Version: a.build.Version, Commit: a.build.Commit, BuildDate: a.build.Date,
		GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
		WorkDir: loaded.ProjectRoot, Model: modelLabel(loaded.Profile()),
		Security: approvalLabel(loaded.Config.Approval.Mode),
	}, runner, tuiSessionSource{service: service}, broker, color)
	finalModel, runErr := a.runTUI(model, a.stdin, a.stdout)
	runner.Close()
	broker.Close()
	closeErr := store.Close()
	if runErr != nil {
		if closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
		return a.runtimeSetupError(fmt.Sprintf("运行交互 TUI 失败：%v", runErr))
	}
	if closeErr != nil {
		return a.runtimeStorageError(fmt.Sprintf("关闭会话存储失败：%v", closeErr))
	}
	if result, ok := finalModel.(interface{ ExitCode() int }); ok {
		return result.ExitCode()
	}
	return ExitOK
}

type interactiveTaskRunner struct {
	mu     sync.Mutex
	ctx    context.Context
	app    *App
	loaded config.Loaded
	broker policy.Broker
	active *interactiveTaskExecution
}

func (runner *interactiveTaskRunner) PrepareTask(task string) (tui.TaskExecution, error) {
	if runner == nil || runner.app == nil || runner.broker == nil {
		return nil, errors.New("交互 TUI 运行入口不可用")
	}
	task = strings.TrimSpace(task)
	switch {
	case task == "":
		return nil, errors.New("任务描述不能为空")
	case !utf8.ValidString(task):
		return nil, errors.New("任务描述必须是有效 UTF-8 文本")
	case len(task) > tui.MaxInteractiveTaskBytes:
		return nil, errors.New("任务描述超过 64 KiB 上限")
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.active != nil {
		return nil, errors.New("当前 TUI 已有任务在运行")
	}
	runID, err := runner.app.newRunID()
	if err != nil {
		return nil, err
	}
	ctx := runner.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	child := *runner.app
	var diagnostics strings.Builder
	child.stdin, child.stdout, child.stderr = strings.NewReader(""), io.Discard, &diagnostics
	execution := &interactiveTaskExecution{
		runner: runner, app: &child, ctx: runCtx, cancel: cancel,
		loaded: runner.loaded, broker: runner.broker, runID: runID,
		sessionID: "ses_" + runID, task: task, diagnostics: &diagnostics,
		done: make(chan struct{}),
	}
	runner.active = execution
	return execution, nil
}

func (runner *interactiveTaskRunner) Close() {
	if runner == nil {
		return
	}
	runner.mu.Lock()
	active := runner.active
	runner.mu.Unlock()
	if active != nil {
		active.stopAndWait()
	}
}

type interactiveTaskExecution struct {
	runner      *interactiveTaskRunner
	app         *App
	ctx         context.Context
	cancel      context.CancelFunc
	loaded      config.Loaded
	broker      policy.Broker
	runID       string
	sessionID   string
	task        string
	diagnostics *strings.Builder
	once        sync.Once
	done        chan struct{}
	result      tui.TaskResult
}

func (execution *interactiveTaskExecution) SessionID() string { return execution.sessionID }

func (execution *interactiveTaskExecution) Run() tui.TaskResult {
	execution.once.Do(func() {
		defer execution.release()
		code := execution.app.runAgent(
			execution.ctx, execution.loaded, execution.runID, execution.task, validatedDiscardSink{},
			runtimeOptions{
				Mode: policy.ModeInteractive, Broker: execution.broker,
				AllowEdit: execution.loaded.Config.Approval.Mode == config.ApprovalAutoEdit,
				Security:  "受控本地执行 · TUI 交互审批", SessionID: execution.sessionID,
			},
		)
		execution.result = tui.TaskResult{ExitCode: code, Detail: strings.TrimSpace(execution.diagnostics.String())}
	})
	<-execution.done
	return execution.result
}

func (execution *interactiveTaskExecution) stopAndWait() {
	execution.Cancel()
	execution.once.Do(func() {
		execution.result = tui.TaskResult{ExitCode: ExitUserCanceled, Detail: "任务在启动完成前被取消"}
		execution.release()
	})
	<-execution.done
}

func (execution *interactiveTaskExecution) release() {
	execution.cancel()
	execution.runner.mu.Lock()
	if execution.runner.active == execution {
		execution.runner.active = nil
	}
	execution.runner.mu.Unlock()
	close(execution.done)
}

func (execution *interactiveTaskExecution) Cancel() {
	if execution != nil && execution.cancel != nil {
		execution.cancel()
	}
}

type validatedDiscardSink struct{}

func (validatedDiscardSink) Emit(ctx context.Context, event events.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return event.Validate()
}

var _ events.Sink = validatedDiscardSink{}
var _ tui.TaskRunner = (*interactiveTaskRunner)(nil)
