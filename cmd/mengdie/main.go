// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/app"
	"github.com/Scorpio69t/mengdie-code/internal/ui/terminal"
)

var (
	version   = "0.0.0-dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt)

	operationCtx, cancelOperation := context.WithCancel(context.Background())
	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	state, err := terminal.NewInterruptState(2 * time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化中断处理失败：%v\n", err)
		os.Exit(app.ExitRunError)
	}
	go func() {
		_ = terminal.HandleInterrupts(monitorCtx, signals, state, time.Now, cancelOperation, func() {
			os.Exit(130)
		})
	}()

	code := runContext(operationCtx, os.Args[1:], os.Stdout, os.Stderr, isTerminal(os.Stdout))
	stopMonitor()
	signal.Stop(signals)
	os.Exit(code)
}

func run(args []string, stdout, stderr io.Writer, interactive bool) int {
	return runContext(context.Background(), args, stdout, stderr, interactive)
}

func runContext(ctx context.Context, args []string, stdout, stderr io.Writer, interactive bool) int {
	application := app.New(app.BuildInfo{
		Version: version,
		Commit:  commit,
		Date:    buildDate,
	}, stdout, stderr)
	return application.Run(ctx, args, interactive)
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
