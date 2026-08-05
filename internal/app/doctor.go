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
		if err := a.writeError("mengdie doctor 不接受位置参数\n"); err != nil {
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
	report := a.buildDoctorReport(loaded)
	if *jsonOutput {
		encoder := json.NewEncoder(a.stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			if writeErr := a.writeError("doctor 输出失败：%v\n", err); writeErr != nil {
				return ExitRunError
			}
			return ExitRunError
		}
		return ExitOK
	}
	if err := a.writeDoctorReport(report); err != nil {
		return ExitRunError
	}
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

func (a *App) writeDoctorReport(report doctorReport) error {
	var apiKeyLine string
	if report.APIKeyEnvironment == "" {
		apiKeyLine = "• API Key：未配置环境变量名"
	} else {
		apiKeyLine = fmt.Sprintf("%s API Key：%s（不显示值）", setMark(report.CredentialSet), report.APIKeyEnvironment)
	}
	_, err := fmt.Fprintf(a.stdout,
		"✓ MengDie Code %s · %s · %s\n"+
			"✓ 项目根：%s\n"+
			"%s 用户配置：%s\n"+
			"%s 项目配置：%s\n"+
			"✓ Profile：%s\n"+
			"• Provider：%s\n"+
			"• 模型：%s\n"+
			"%s\n"+
			"✓ 审批模式：%s\n"+
			"✓ 最大回合：%d\n"+
			"• Provider 网络与认证探测将在 P1-10 实现\n",
		report.Version,
		report.Platform,
		report.GoVersion,
		report.ProjectRoot,
		loadedMark(report.UserConfigLoaded),
		report.UserConfigPath,
		loadedMark(report.ProjectConfigLoaded),
		report.ProjectConfigPath,
		report.Profile,
		fallback(report.Provider, "未配置"),
		fallback(report.Model, "未配置"),
		apiKeyLine,
		report.Approval,
		report.MaxTurns,
	)
	return err
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
