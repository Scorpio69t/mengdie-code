// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"
)

const version = "0.0.0-dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

func run(args []string, stdout io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			fmt.Fprintf(stdout, "MengDie Code %s\n", version)
			return 0
		}
	}

	fmt.Fprintln(stdout, "MengDie Code 正处于早期开发阶段，Agent 功能尚未实现。")
	fmt.Fprintln(stdout, "请阅读 README.md 与 ARCHITECTURE.md 了解当前计划。")
	return 0
}
