// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package app owns command dispatch and application-level dependency wiring.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/brand"
	"github.com/Scorpio69t/mengdie-code/internal/config"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/ui/terminal"
)

const (
	ExitOK           = 0
	ExitRunError     = 1
	ExitInvalidInput = 2
	ExitUserCanceled = 5
)

// BuildInfo contains values injected by the release build.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// App dispatches CLI commands using explicit process dependencies.
type App struct {
	build         BuildInfo
	stdout        io.Writer
	stderr        io.Writer
	lookupEnv     func(string) (string, bool)
	userConfigDir string
	now           func() time.Time
	newRunID      func() (string, error)
}

// New constructs the production application service.
func New(build BuildInfo, stdout, stderr io.Writer) *App {
	return &App{
		build:     build,
		stdout:    stdout,
		stderr:    stderr,
		lookupEnv: os.LookupEnv,
		now:       time.Now,
		newRunID:  events.NewRunID,
	}
}

// Run dispatches one top-level command.
func (a *App) Run(ctx context.Context, args []string, interactive bool) int {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			a.writeVersion()
			return ExitOK
		case "doctor":
			return a.runDoctor(ctx, args[1:])
		case "exec":
			return a.runExec(ctx, args[1:])
		}
		if !strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(a.stderr, "未知命令 %q\n", args[0])
			return ExitInvalidInput
		}
	}
	return a.runInteractive(ctx, args, interactive)
}

func (a *App) runInteractive(_ context.Context, args []string, interactive bool) int {
	flags, common := a.newCommonFlagSet("mengdie")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(a.stderr, "交互模式不接受位置参数")
		return ExitInvalidInput
	}
	loaded, err := a.loadConfig(common)
	if err != nil {
		fmt.Fprintf(a.stderr, "配置错误：%v\n", err)
		return ExitInvalidInput
	}
	profile := loaded.Profile()
	if interactive {
		if err := brand.WriteWelcome(a.stdout, brand.Info{
			Version:   a.build.Version,
			Commit:    a.build.Commit,
			BuildDate: a.build.Date,
			GoVersion: runtime.Version(),
			Platform:  runtime.GOOS + "/" + runtime.GOARCH,
			WorkDir:   loaded.ProjectRoot,
			Model:     modelLabel(profile),
			Security:  approvalLabel(loaded.Config.Approval.Mode),
		}); err != nil {
			return ExitRunError
		}
	}
	fmt.Fprintln(a.stdout, "当前阶段：P1-00 / P1-01 / P1-02 开发预览，Agent 功能尚未实现。")
	fmt.Fprintln(a.stdout, "可运行 mengdie doctor 检查当前配置。")
	return ExitOK
}

func (a *App) runExec(ctx context.Context, args []string) int {
	flags, common := a.newCommonFlagSet("mengdie exec")
	jsonOutput := flags.Bool("json", false, "输出 JSON Lines 事件")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	task := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if task == "" {
		fmt.Fprintln(a.stderr, "mengdie exec 需要任务描述")
		return ExitInvalidInput
	}
	loaded, err := a.loadConfig(common)
	if err != nil {
		fmt.Fprintf(a.stderr, "配置错误：%v\n", err)
		return ExitInvalidInput
	}
	runID, err := a.newRunID()
	if err != nil {
		fmt.Fprintf(a.stderr, "创建 Run 失败：%v\n", err)
		return ExitRunError
	}
	var sink events.Sink
	if *jsonOutput {
		sink, err = terminal.NewJSONRenderer(a.stdout)
	} else {
		sink, err = terminal.NewHumanRenderer(a.stderr)
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "初始化输出失败：%v\n", err)
		return ExitRunError
	}
	emitter, err := events.NewEmitter(runID, sink, a.now)
	if err != nil {
		fmt.Fprintf(a.stderr, "初始化事件流失败：%v\n", err)
		return ExitRunError
	}
	profile := loaded.Profile()
	if _, err := emitter.Emit(ctx, events.KindRunStarted, events.RunStarted{
		Model:    modelLabel(profile),
		CWD:      loaded.ProjectRoot,
		Security: approvalLabel(loaded.Config.Approval.Mode),
	}); err != nil {
		fmt.Fprintf(a.stderr, "输出事件失败：%v\n", err)
		return emitExitCode(err)
	}
	if _, err := emitter.Emit(ctx, events.KindRunFailed, events.RunFailed{
		Category: "runtime_unavailable",
		Message:  "Agent Runtime 尚未实现；P1-02 仅提供事件与终端输出骨架",
	}); err != nil {
		fmt.Fprintf(a.stderr, "输出事件失败：%v\n", err)
		return emitExitCode(err)
	}
	return ExitRunError
}

func (a *App) writeVersion() {
	fmt.Fprintf(a.stdout, "MengDie Code %s\n", a.build.Version)
	fmt.Fprintf(a.stdout, "commit %s\n", a.build.Commit)
	fmt.Fprintf(a.stdout, "built %s\n", a.build.Date)
	fmt.Fprintf(a.stdout, "go %s\n", runtime.Version())
	fmt.Fprintf(a.stdout, "platform %s/%s\n", runtime.GOOS, runtime.GOARCH)
}

type commonFlags struct {
	cwd       string
	profile   string
	model     string
	approval  string
	maxTurns  int
	debugMode bool
}

func (a *App) newCommonFlagSet(name string) (*flag.FlagSet, *commonFlags) {
	values := &commonFlags{}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.StringVar(&values.cwd, "cwd", "", "项目工作目录")
	flags.StringVar(&values.profile, "profile", "", "配置 profile")
	flags.StringVar(&values.model, "model", "", "模型，格式为 provider:model")
	flags.StringVar(&values.approval, "approval", "", "审批模式 suggest 或 auto-edit")
	flags.IntVar(&values.maxTurns, "max-turns", 0, "最大 Agent 回合数")
	flags.BoolVar(&values.debugMode, "debug", false, "输出脱敏诊断日志")
	return flags, values
}

func (a *App) loadConfig(common *commonFlags) (config.Loaded, error) {
	return config.Load(config.Options{
		WorkDir:          common.cwd,
		UserConfigDir:    a.userConfigDir,
		LookupEnv:        a.lookupEnv,
		ProfileOverride:  strings.TrimSpace(common.profile),
		ModelOverride:    strings.TrimSpace(common.model),
		ApprovalOverride: strings.TrimSpace(common.approval),
		MaxTurnsOverride: common.maxTurns,
	})
}

func modelLabel(profile config.Profile) string {
	if profile.Provider == "" && profile.Model == "" {
		return "未配置"
	}
	return profile.Provider + ":" + profile.Model
}

func approvalLabel(mode string) string {
	switch mode {
	case config.ApprovalAutoEdit:
		return "自动编辑 · 工具执行尚未启用"
	default:
		return "建议模式 · 工具执行尚未启用"
	}
}

func flagExitCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return ExitOK
	}
	return ExitInvalidInput
}

func emitExitCode(err error) int {
	if errors.Is(err, context.Canceled) {
		return ExitUserCanceled
	}
	return ExitRunError
}
