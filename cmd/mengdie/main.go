// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/Scorpio69t/mengdie-code/internal/brand"
)

var (
	version   = "0.0.0-dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, isTerminal(os.Stdout)))
}

func run(args []string, stdout io.Writer, interactive bool) int {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			writeVersion(stdout)
			return 0
		}
	}

	workDir, err := os.Getwd()
	if err != nil {
		workDir = "不可用"
	}
	if interactive {
		if err := brand.WriteWelcome(stdout, brand.Info{
			Version:   version,
			Commit:    commit,
			BuildDate: buildDate,
			GoVersion: runtime.Version(),
			Platform:  runtime.GOOS + "/" + runtime.GOARCH,
			WorkDir:   workDir,
			Model:     "未配置",
			Security:  "工具执行尚未启用",
		}); err != nil {
			return 1
		}
	}

	fmt.Fprintln(stdout, "当前阶段：架构与基础设施，Agent 功能尚未实现。")
	fmt.Fprintln(stdout, "请阅读 README.md 与 ARCHITECTURE.md 了解当前计划。")
	return 0
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func writeVersion(w io.Writer) {
	fmt.Fprintf(w, "MengDie Code %s\n", version)
	fmt.Fprintf(w, "commit %s\n", commit)
	fmt.Fprintf(w, "built %s\n", buildDate)
	fmt.Fprintf(w, "go %s\n", runtime.Version())
	fmt.Fprintf(w, "platform %s/%s\n", runtime.GOOS, runtime.GOARCH)
}
