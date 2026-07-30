// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"

	"github.com/Scorpio69t/mengdie-code/internal/app"
)

var (
	version   = "0.0.0-dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, isTerminal(os.Stdout)))
}

func run(args []string, stdout, stderr io.Writer, interactive bool) int {
	application := app.New(app.BuildInfo{
		Version: version,
		Commit:  commit,
		Date:    buildDate,
	}, stdout, stderr)
	return application.Run(context.Background(), args, interactive)
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

