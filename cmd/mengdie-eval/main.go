// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Scorpio69t/mengdie-code/internal/evaluation"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mengdie-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "evals/coding/smoke.json", "评测 manifest 路径")
	pretty := flags.Bool("pretty", false, "格式化 JSON 输出")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		if _, err := fmt.Fprintln(stderr, "mengdie-eval 不接受位置参数"); err != nil {
			return 1
		}
		return 2
	}

	result, err := evaluation.RunBaseline(ctx, *manifestPath)
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "评测启动失败：%v\n", err); writeErr != nil {
			return 1
		}
		return 2
	}
	encoder := json.NewEncoder(stdout)
	if *pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(result); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "评测结果输出失败：%v\n", err); writeErr != nil {
			return 1
		}
		return 1
	}
	if !result.Passed {
		return 1
	}
	return 0
}
