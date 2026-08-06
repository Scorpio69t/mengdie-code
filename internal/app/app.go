// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package app owns command dispatch and application-level dependency wiring.
package app

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Scorpio69t/mengdie-code/internal/brand"
	"github.com/Scorpio69t/mengdie-code/internal/config"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/ui/terminal"
)

const (
	ExitOK            = 0
	ExitRunError      = 1
	ExitInvalidInput  = 2
	ExitProviderError = 3
	ExitPolicyDenied  = 4
	ExitUserCanceled  = 5
	ExitToolFailure   = 6

	maxInteractiveTaskBytes = 64 << 10
)

var (
	errInteractiveTaskEmpty    = errors.New("任务描述不能为空")
	errInteractiveTaskTooLarge = errors.New("任务描述超过 64 KiB 上限")
	errInteractiveTaskEncoding = errors.New("任务描述必须是有效 UTF-8 文本")
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
	stdin         io.Reader
	stdout        io.Writer
	stderr        io.Writer
	lookupEnv     func(string) (string, bool)
	environment   func() []string
	newProvider   providerFactory
	userConfigDir string
	dataDir       string
	now           func() time.Time
	newRunID      func() (string, error)
}

// New constructs the production application service.
func New(build BuildInfo, stdout, stderr io.Writer) *App {
	return &App{
		build:       build,
		stdin:       os.Stdin,
		stdout:      stdout,
		stderr:      stderr,
		lookupEnv:   os.LookupEnv,
		environment: os.Environ,
		newProvider: defaultProviderFactory,
		now:         time.Now,
		newRunID:    events.NewRunID,
	}
}

// Run dispatches one top-level command.
func (a *App) Run(ctx context.Context, args []string, interactive bool) int {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			if err := a.writeVersion(); err != nil {
				return ExitRunError
			}
			return ExitOK
		case "doctor":
			return a.runDoctor(ctx, args[1:], interactive)
		case "exec":
			return a.runExec(ctx, args[1:])
		}
		if !strings.HasPrefix(args[0], "-") {
			if err := a.writeError("未知命令 %q\n", args[0]); err != nil {
				return ExitRunError
			}
			return ExitInvalidInput
		}
	}
	return a.runInteractive(ctx, args, interactive)
}

func (a *App) runInteractive(ctx context.Context, args []string, interactive bool) int {
	flags, common := a.newCommonFlagSet("mengdie")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		if err := a.writeError("交互模式不接受位置参数\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	if !interactive {
		if err := a.writeError("交互模式需要终端输入和输出；请使用 mengdie exec <任务> 运行重定向或自动化任务\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	loaded, err := a.loadConfig(common)
	if err != nil {
		if writeErr := a.writeError("配置错误：%v\n", err); writeErr != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	profile := loaded.Profile()
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
	if err := ctx.Err(); err != nil {
		return emitExitCode(err)
	}
	reader := bufio.NewReader(a.stdin)
	if _, err := fmt.Fprint(a.stdout, "请输入任务（单次有界运行，Ctrl+C 取消）："); err != nil {
		return ExitRunError
	}
	task, err := readInteractiveTask(ctx, reader)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return ExitUserCanceled
		}
		if writeErr := a.writeError("读取交互任务失败：%v\n", err); writeErr != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	if _, err := fmt.Fprintln(a.stdout); err != nil {
		return ExitRunError
	}
	runID, err := a.newRunID()
	if err != nil {
		if writeErr := a.writeError("创建 Run 失败：%v\n", err); writeErr != nil {
			return ExitRunError
		}
		return ExitRunError
	}
	renderer, err := terminal.NewHumanRenderer(a.stdout)
	if err != nil {
		if writeErr := a.writeError("初始化输出失败：%v\n", err); writeErr != nil {
			return ExitRunError
		}
		return ExitRunError
	}
	broker, err := policy.NewTextBroker(reader, a.stdout)
	if err != nil {
		return a.runtimeSetupError(fmt.Sprintf("初始化审批输入失败：%v", err))
	}
	return a.runAgent(ctx, loaded, runID, task, renderer, runtimeOptions{
		Mode:      policy.ModeInteractive,
		Broker:    broker,
		AllowEdit: loaded.Config.Approval.Mode == config.ApprovalAutoEdit,
		Security:  "受控本地执行 · 交互审批",
	})
}

func readInteractiveTask(ctx context.Context, reader *bufio.Reader) (string, error) {
	if reader == nil {
		return "", errors.New("交互输入不可用")
	}
	var builder strings.Builder
	tooLong := false
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		part, more, err := reader.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && builder.Len() > 0 {
				break
			}
			if errors.Is(err, io.EOF) {
				return "", errInteractiveTaskEmpty
			}
			return "", fmt.Errorf("读取标准输入：%w", err)
		}
		if builder.Len()+len(part) > maxInteractiveTaskBytes {
			tooLong = true
		} else if !tooLong {
			builder.Write(part)
		}
		if !more {
			break
		}
	}
	if tooLong {
		return "", errInteractiveTaskTooLarge
	}
	if !utf8.ValidString(builder.String()) {
		return "", errInteractiveTaskEncoding
	}
	task := strings.TrimSpace(builder.String())
	if task == "" {
		return "", errInteractiveTaskEmpty
	}
	return task, nil
}

func (a *App) runExec(ctx context.Context, args []string) int {
	flags, common := a.newCommonFlagSet("mengdie exec")
	jsonOutput := flags.Bool("json", false, "输出 JSON Lines 事件")
	allowEdit := flags.Bool("allow-edit", false, "允许本次无头任务修改项目文件")
	var allowCommands commandPrefixFlag
	var allowEnvironment stringListFlag
	flags.Var(&allowCommands, "allow-command", "允许的非交互命令前缀，可重复；go,test 表示 go test")
	flags.Var(&allowEnvironment, "allow-env", "允许 shell 继承的敏感环境变量名，可重复或用逗号分隔")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	task := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if task == "" {
		if err := a.writeError("mengdie exec 需要任务描述\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	loaded, err := a.loadConfig(common)
	if err != nil {
		if writeErr := a.writeError("配置错误：%v\n", err); writeErr != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return emitExitCode(err)
	}
	runID, err := a.newRunID()
	if err != nil {
		if writeErr := a.writeError("创建 Run 失败：%v\n", err); writeErr != nil {
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
	return a.runAgent(ctx, loaded, runID, task, sink, runtimeOptions{
		Mode: policy.ModeHeadless, Security: "受控本地执行 · 无头模式",
		AllowEdit: *allowEdit, AllowCommands: allowCommands.Values(),
		AllowedEnvironment: allowEnvironment.Values(),
	})
}

func (a *App) writeVersion() error {
	_, err := fmt.Fprintf(a.stdout,
		"MengDie Code %s\ncommit %s\nbuilt %s\ngo %s\nplatform %s/%s\n",
		a.build.Version,
		a.build.Commit,
		a.build.Date,
		runtime.Version(),
		runtime.GOOS,
		runtime.GOARCH,
	)
	return err
}

func (a *App) writeError(format string, args ...any) error {
	_, err := fmt.Fprintf(a.stderr, format, args...)
	return err
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
		return "自动编辑 · 命令按规则审批"
	default:
		return "建议模式 · 副作用按规则审批"
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
