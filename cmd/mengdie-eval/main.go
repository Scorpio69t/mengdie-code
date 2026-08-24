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
	"path/filepath"

	"github.com/Scorpio69t/mengdie-code/internal/evaluation"
	"github.com/Scorpio69t/mengdie-code/internal/evaluation/chaos"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runBaseline(ctx, []string{}, stdout, stderr)
	}
	switch args[0] {
	case "baseline":
		return runBaseline(ctx, args[1:], stdout, stderr)
	case "chaos":
		return runChaos(ctx, args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "mengdie-eval: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runBaseline(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mengdie-eval baseline", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "evals/coding/smoke.json", "评测 manifest 路径")
	pretty := flags.Bool("pretty", false, "格式化 JSON 输出")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "mengdie-eval baseline 不接受位置参数")
		return 2
	}
	result, err := evaluation.RunBaseline(ctx, *manifestPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "评测启动失败：%v\n", err)
		return 2
	}
	encoder := json.NewEncoder(stdout)
	if *pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(result); err != nil {
		_, _ = fmt.Fprintf(stderr, "评测结果输出失败：%v\n", err)
		return 1
	}
	if !result.Passed {
		return 1
	}
	return 0
}

func runChaos(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mengdie-eval chaos", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "evals/chaos/all.json", "随机故障注入 manifest 路径")
	rounds := flags.Int("rounds", 1, "每个场景执行轮数")
	seed := flags.Int64("seed", 1, "调度种子基准")
	outPath := flags.String("out", "", "可选的证据输出文件路径，默认写入 stdout")
	pretty := flags.Bool("pretty", false, "格式化 JSON 输出")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "mengdie-eval chaos 不接受位置参数")
		return 2
	}
	absoluteManifest, err := filepath.Abs(*manifestPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "解析 manifest 路径失败：%v\n", err)
		return 2
	}
	matrix, err := chaos.RunManifest(ctx, absoluteManifest, *rounds, *seed)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "随机故障注入启动失败：%v\n", err)
		return 2
	}
	encoder := json.NewEncoder(stdout)
	if *pretty {
		encoder.SetIndent("", "  ")
	}
	if *outPath != "" {
		file, err := os.Create(*outPath)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "打开证据输出文件失败：%v\n", err)
			return 2
		}
		defer func() {
			_ = file.Close()
		}()
		encoder = json.NewEncoder(file)
		if *pretty {
			encoder.SetIndent("", "  ")
		}
	}
	if err := encoder.Encode(matrix); err != nil {
		_, _ = fmt.Fprintf(stderr, "随机故障注入结果输出失败：%v\n", err)
		return 1
	}
	if !matrix.Passed {
		return 1
	}
	return 0
}
