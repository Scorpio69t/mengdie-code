// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/Scorpio69t/mengdie-code/internal/config"
)

type doctorReport struct {
	Status              string `json:"status"`
	Version             string `json:"version"`
	Commit              string `json:"commit"`
	GoVersion           string `json:"go_version"`
	Platform            string `json:"platform"`
	ProjectRoot         string `json:"project_root"`
	UserConfigPath      string `json:"user_config_path"`
	UserConfigLoaded    bool   `json:"user_config_loaded"`
	ProjectConfigPath   string `json:"project_config_path"`
	ProjectConfigLoaded bool   `json:"project_config_loaded"`
	Profile             string `json:"profile"`
	Provider            string `json:"provider,omitempty"`
	Model               string `json:"model,omitempty"`
	APIKeyEnvironment   string `json:"api_key_environment,omitempty"`
	CredentialSet       bool   `json:"credential_set"`
	Approval            string `json:"approval"`
	MaxTurns            int    `json:"max_turns"`
}

func (a *App) runDoctor(_ context.Context, args []string) int {
	flags, common := a.newCommonFlagSet("mengdie doctor")
	jsonOutput := flags.Bool("json", false, "输出 JSON")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(a.stderr, "mengdie doctor 不接受位置参数")
		return ExitInvalidInput
	}
	loaded, err := a.loadConfig(common)
	if err != nil {
		fmt.Fprintf(a.stderr, "配置错误：%v\n", err)
		return ExitInvalidInput
	}
	report := a.buildDoctorReport(loaded)
	if *jsonOutput {
		encoder := json.NewEncoder(a.stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(a.stderr, "doctor 输出失败：%v\n", err)
			return ExitRunError
		}
		return ExitOK
	}
	a.writeDoctorReport(report)
	return ExitOK
}

func (a *App) buildDoctorReport(loaded config.Loaded) doctorReport {
	profile := loaded.Profile()
	credentialSet := false
	if profile.APIKeyEnv != "" {
		value, exists := a.lookupEnv(profile.APIKeyEnv)
		credentialSet = exists && value != ""
	}
	return doctorReport{
		Status:              "ok",
		Version:             a.build.Version,
		Commit:              a.build.Commit,
		GoVersion:           runtime.Version(),
		Platform:            runtime.GOOS + "/" + runtime.GOARCH,
		ProjectRoot:         loaded.ProjectRoot,
		UserConfigPath:      loaded.UserConfigPath,
		UserConfigLoaded:    loaded.UserConfigLoaded,
		ProjectConfigPath:   loaded.ProjectConfigPath,
		ProjectConfigLoaded: loaded.ProjectConfigLoaded,
		Profile:             loaded.SelectedProfile,
		Provider:            profile.Provider,
		Model:               profile.Model,
		APIKeyEnvironment:   profile.APIKeyEnv,
		CredentialSet:       credentialSet,
		Approval:            loaded.Config.Approval.Mode,
		MaxTurns:            loaded.Config.Context.MaxTurns,
	}
}

func (a *App) writeDoctorReport(report doctorReport) {
	fmt.Fprintf(a.stdout, "✓ MengDie Code %s · %s · %s\n", report.Version, report.Platform, report.GoVersion)
	fmt.Fprintf(a.stdout, "✓ 项目根：%s\n", report.ProjectRoot)
	fmt.Fprintf(a.stdout, "%s 用户配置：%s\n", loadedMark(report.UserConfigLoaded), report.UserConfigPath)
	fmt.Fprintf(a.stdout, "%s 项目配置：%s\n", loadedMark(report.ProjectConfigLoaded), report.ProjectConfigPath)
	fmt.Fprintf(a.stdout, "✓ Profile：%s\n", report.Profile)
	fmt.Fprintf(a.stdout, "• Provider：%s\n", fallback(report.Provider, "未配置"))
	fmt.Fprintf(a.stdout, "• 模型：%s\n", fallback(report.Model, "未配置"))
	if report.APIKeyEnvironment == "" {
		fmt.Fprintln(a.stdout, "• API Key：未配置环境变量名")
	} else {
		fmt.Fprintf(a.stdout, "%s API Key：%s（不显示值）\n", setMark(report.CredentialSet), report.APIKeyEnvironment)
	}
	fmt.Fprintf(a.stdout, "✓ 审批模式：%s\n", report.Approval)
	fmt.Fprintf(a.stdout, "✓ 最大回合：%d\n", report.MaxTurns)
	fmt.Fprintln(a.stdout, "• Provider 网络与认证探测将在 P1-10 实现")
}

func loadedMark(loaded bool) string {
	if loaded {
		return "✓ 已加载"
	}
	return "• 未找到"
}

func setMark(set bool) string {
	if set {
		return "✓ 已设置"
	}
	return "! 未设置"
}

func fallback(value, fallbackValue string) string {
	if value == "" {
		return fallbackValue
	}
	return value
}