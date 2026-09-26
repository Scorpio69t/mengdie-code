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
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/app"
	"github.com/Scorpio69t/mengdie-code/internal/evaluation"
	"github.com/Scorpio69t/mengdie-code/internal/evaluation/chaos"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
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
	case "repo":
		return runRepo(ctx, args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "mengdie-eval: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runRepo(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "用法：mengdie-eval repo validate|baseline|run ...")
		return 2
	}
	switch args[0] {
	case "validate":
		return runRepoValidate(ctx, args[1:], stdout, stderr)
	case "baseline":
		return runRepoBaseline(ctx, args[1:], stdout, stderr)
	case "run":
		return runRepoDiagnostic(ctx, args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintln(stderr, "用法：mengdie-eval repo validate|baseline|run ...")
		return 2
	}
}

type commandPrefixValues []string

func (values *commandPrefixValues) String() string { return strings.Join(*values, ", ") }
func (values *commandPrefixValues) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("agent command prefix cannot be empty")
	}
	*values = append(*values, value)
	return nil
}

func runRepoDiagnostic(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mengdie-eval repo run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "已审核的真实仓库 manifest 路径")
	taskID := flags.String("task", "", "单个任务 ID")
	verifierBin := flags.String("verifier-bin", "", "操作者选择的 verifier 可执行文件绝对路径")
	profile := flags.String("profile", "", "用户配置中的 Provider profile")
	allowAgentEdit := flags.Bool("allow-agent-edit", false, "操作者允许 Agent 编辑任务白名单文件")
	allowUnisolated := flags.Bool("allow-unisolated", false, "确认在当前账户下执行不可信仓库代码；本地诊断无 OS 沙箱")
	pretty := flags.Bool("pretty", false, "格式化 JSON 输出")
	var commands commandPrefixValues
	flags.Var(&commands, "agent-command", "操作者批准的 Agent 命令前缀，可重复；默认不允许 Shell")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *manifestPath == "" || *taskID == "" || *verifierBin == "" || !*allowUnisolated {
		_, _ = fmt.Fprintln(stderr, "用法：mengdie-eval repo run --manifest <path> --task <id> --verifier-bin <absolute-path> --allow-unisolated [--profile <name>] [--agent-command 'go test'] [--pretty]")
		return 2
	}
	manifest, err := evaluation.LoadRealRepositoryManifest(*manifestPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "真实仓库任务定义无效")
		return 2
	}
	application := app.New(app.BuildInfo{Version: "eval-local"}, io.Discard, io.Discard)
	result, err := evaluation.RunRealRepositoryDiagnostic(ctx, manifest, *taskID, evaluation.RealRepositoryRunOptions{
		VerifierExecutable: *verifierBin, CommandPrefixes: commands, AllowAgentEdit: *allowAgentEdit,
		Agent: func(runCtx context.Context, run evaluation.RealAgentRunOptions) (evaluation.RealAgentRunEvidence, error) {
			environment, err := evaluation.RealAgentEnvironment(*verifierBin, run.StateDir)
			if err != nil {
				return evaluation.RealAgentRunEvidence{Status: "agent_failed"}, err
			}
			effects := make([]tools.Effect, len(run.Budget.AllowedEffects))
			for index, effect := range run.Budget.AllowedEffects {
				effects[index] = tools.Effect(effect)
			}
			agentResult, err := application.RunEvaluation(runCtx, app.EvaluationRunOptions{
				Workspace: run.Workspace, StateDir: run.StateDir, Profile: *profile,
				Prompt: run.Prompt, Environment: environment,
				Policy: app.EvaluationPolicy{AllowedEffects: effects, AllowedChanges: run.AllowedChanges, AllowEdit: run.AllowEdit, CommandPrefixes: run.CommandPrefixes,
					MaxToolCalls: run.Budget.MaxToolCalls, MaxCommandCalls: run.Budget.MaxCommandCalls},
			})
			return evaluation.RealAgentRunEvidence{Status: agentResult.Status, ToolCalls: agentResult.ToolCalls,
				CommandCalls: agentResult.CommandCalls, DeniedTools: agentResult.DeniedTools,
				ForcedCleanupCount:    agentResult.ForcedCleanupCount,
				UncertainExecuteCount: agentResult.UncertainExecuteCount,
				InputTokens:           agentResult.InputTokens, OutputTokens: agentResult.OutputTokens}, err
		},
	})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "真实仓库本地诊断预检失败")
		return 2
	}
	encoder := json.NewEncoder(stdout)
	if *pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(result); err != nil {
		_, _ = fmt.Fprintln(stderr, "真实仓库本地诊断证据输出失败")
		return 1
	}
	if result.Status != "diagnostic_passed" {
		return 1
	}
	return 0
}

func runRepoValidate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mengdie-eval repo validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "真实仓库评测 manifest 路径")
	pretty := flags.Bool("pretty", false, "格式化 JSON 输出")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "mengdie-eval repo validate 不接受位置参数")
		return 2
	}
	if strings.TrimSpace(*manifestPath) == "" {
		_, _ = fmt.Fprintln(stderr, "缺少 --manifest 参数")
		return 2
	}
	select {
	case <-ctx.Done():
		_, _ = fmt.Fprintf(stderr, "评测 manifest 校验已取消：%v\n", ctx.Err())
		return 1
	default:
	}
	manifest, err := evaluation.LoadRealRepositoryManifest(*manifestPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "真实仓库任务定义无效：%v\n", err)
		return 2
	}
	if err := evaluation.EncodeRealRepositorySummary(stdout, manifest, *pretty); err != nil {
		_, _ = fmt.Fprintf(stderr, "评测摘要输出失败：%v\n", err)
		return 1
	}
	return 0
}

func runRepoBaseline(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mengdie-eval repo baseline", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "已审核的真实仓库 manifest 路径")
	taskID := flags.String("task", "", "单个任务 ID")
	verifierBin := flags.String("verifier-bin", "", "操作者选择的 verifier 可执行文件绝对路径")
	allowUnisolated := flags.Bool("allow-unisolated", false, "确认在当前账户下执行不可信仓库代码；本地诊断无 OS 沙箱")
	pretty := flags.Bool("pretty", false, "格式化 JSON 输出")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*manifestPath) == "" || strings.TrimSpace(*taskID) == "" || strings.TrimSpace(*verifierBin) == "" || !*allowUnisolated {
		_, _ = fmt.Fprintln(stderr, "用法：mengdie-eval repo baseline --manifest <path> --task <id> --verifier-bin <absolute-path> --allow-unisolated [--pretty]")
		return 2
	}
	if err := ctx.Err(); err != nil {
		_, _ = fmt.Fprintf(stderr, "真实仓库基线诊断已取消：%v\n", err)
		return 1
	}
	manifest, err := evaluation.LoadRealRepositoryManifest(*manifestPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "真实仓库任务定义无效：%v\n", err)
		return 2
	}
	result, err := evaluation.RunRealRepositoryBaseline(ctx, manifest, *taskID, evaluation.RealRepositoryBaselineOptions{VerifierExecutable: *verifierBin})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "真实仓库基线预检失败：%v\n", err)
		return 2
	}
	encoder := json.NewEncoder(stdout)
	if *pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(result); err != nil {
		_, _ = fmt.Fprintf(stderr, "真实仓库基线证据输出失败：%v\n", err)
		return 1
	}
	if result.Status != "baseline_matched" {
		return 1
	}
	return 0
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
