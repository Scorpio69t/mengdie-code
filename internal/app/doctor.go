// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/config"
	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/project"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/skills"
)

const (
	doctorSchemaVersion = 1
	doctorProbeToken    = "MENGDIE_DOCTOR_OK"
	doctorProbeTool     = "doctor_echo"
	defaultProbeTimeout = 15 * time.Second
)

type doctorCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type doctorCapabilities struct {
	ToolCalling      bool `json:"tool_calling"`
	ParallelTools    bool `json:"parallel_tools"`
	UsageInStream    bool `json:"usage_in_stream"`
	StrictToolSchema bool `json:"strict_tool_schema"`
	MaxContextTokens int  `json:"max_context_tokens"`
}

type doctorProviderProbe struct {
	Attempted   bool   `json:"attempted"`
	Status      string `json:"status"`
	Category    string `json:"category,omitempty"`
	Code        string `json:"code,omitempty"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
	ToolCalling bool   `json:"tool_calling"`
}

type doctorShell struct {
	Available  bool   `json:"available"`
	Name       string `json:"name,omitempty"`
	Executable string `json:"executable,omitempty"`
}

type doctorTool struct {
	Available  bool   `json:"available"`
	Executable string `json:"executable,omitempty"`
	Fallback   string `json:"fallback,omitempty"`
}

type doctorTerminal struct {
	TTY              bool `json:"tty"`
	Color            bool `json:"color"`
	InteractiveInput bool `json:"interactive_input"`
}

type doctorGit struct {
	Repository     bool   `json:"repository"`
	Executable     string `json:"executable,omitempty"`
	TrackedChanges *bool  `json:"tracked_changes,omitempty"`
}

type doctorSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Scope       string `json:"scope"`
}

type doctorSkillConflict struct {
	Name          string `json:"name"`
	WinnerSource  string `json:"winner_source"`
	IgnoredSource string `json:"ignored_source"`
}

type doctorReport struct {
	SchemaVersion       int                   `json:"schema_version"`
	Status              string                `json:"status"`
	Offline             bool                  `json:"offline"`
	Version             string                `json:"version"`
	Commit              string                `json:"commit"`
	GoVersion           string                `json:"go_version"`
	Platform            string                `json:"platform"`
	ProjectRoot         string                `json:"project_root"`
	ProjectName         string                `json:"project_name"`
	UserConfigPath      string                `json:"user_config_path"`
	UserConfigLoaded    bool                  `json:"user_config_loaded"`
	ProjectConfigPath   string                `json:"project_config_path"`
	ProjectConfigLoaded bool                  `json:"project_config_loaded"`
	Profile             string                `json:"profile"`
	Provider            string                `json:"provider,omitempty"`
	ProviderEndpoint    string                `json:"provider_endpoint,omitempty"`
	Model               string                `json:"model,omitempty"`
	APIKeyEnvironment   string                `json:"api_key_environment,omitempty"`
	CredentialSet       bool                  `json:"credential_set"`
	Approval            string                `json:"approval"`
	Security            string                `json:"security"`
	MaxTurns            int                   `json:"max_turns"`
	Agents              []string              `json:"agents"`
	Skills              []doctorSkill         `json:"skills"`
	SkillConflicts      []doctorSkillConflict `json:"skill_conflicts"`
	Shell               doctorShell           `json:"shell"`
	Ripgrep             doctorTool            `json:"ripgrep"`
	Terminal            doctorTerminal        `json:"terminal"`
	Git                 doctorGit             `json:"git"`
	Capabilities        *doctorCapabilities   `json:"capabilities,omitempty"`
	ProviderProbe       doctorProviderProbe   `json:"provider_probe"`
	Checks              []doctorCheck         `json:"checks"`
}

type doctorOptions struct {
	offline      bool
	interactive  bool
	probeTimeout time.Duration
}

func (a *App) runDoctor(ctx context.Context, args []string, interactive bool) int {
	flags, common := a.newCommonFlagSet("mengdie doctor")
	jsonOutput := flags.Bool("json", false, "输出 JSON")
	offline := flags.Bool("offline", false, "只运行本地检查，不连接 Provider")
	probeTimeout := flags.Duration("probe-timeout", defaultProbeTimeout, "Provider 探测超时")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		if err := a.writeError("mengdie doctor 不接受位置参数\n"); err != nil {
			return ExitRunError
		}
		return ExitInvalidInput
	}
	if *probeTimeout < time.Second || *probeTimeout > time.Minute {
		if err := a.writeError("probe-timeout 必须在 1s 到 1m 之间\n"); err != nil {
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
	report := a.buildDoctorReport(ctx, loaded, doctorOptions{
		offline: *offline, interactive: interactive, probeTimeout: *probeTimeout,
	})
	if *jsonOutput {
		encoder := json.NewEncoder(a.stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			if writeErr := a.writeError("doctor 输出失败：%v\n", err); writeErr != nil {
				return ExitRunError
			}
			return ExitRunError
		}
	} else if err := a.writeDoctorReport(report); err != nil {
		return ExitRunError
	}
	return doctorExitCode(report)
}

func (a *App) buildDoctorReport(ctx context.Context, loaded config.Loaded, options doctorOptions) doctorReport {
	profile := loaded.Profile()
	credentialSet := false
	if profile.APIKeyEnv != "" {
		value, exists := a.lookupEnv(profile.APIKeyEnv)
		credentialSet = exists && strings.TrimSpace(value) != ""
	}
	report := doctorReport{
		SchemaVersion: doctorSchemaVersion, Offline: options.offline,
		Version: a.build.Version, Commit: a.build.Commit, GoVersion: runtime.Version(),
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		ProjectRoot: "$PROJECT_ROOT", ProjectName: filepath.Base(loaded.ProjectRoot),
		UserConfigPath: doctorPath(loaded.UserConfigPath, loaded), UserConfigLoaded: loaded.UserConfigLoaded,
		ProjectConfigPath: doctorPath(loaded.ProjectConfigPath, loaded), ProjectConfigLoaded: loaded.ProjectConfigLoaded,
		Profile: loaded.SelectedProfile, Provider: profile.Provider, ProviderEndpoint: providerOrigin(profile.BaseURL),
		Model: profile.Model, APIKeyEnvironment: profile.APIKeyEnv, CredentialSet: credentialSet,
		Approval: loaded.Config.Approval.Mode, Security: "受控本地执行（非强沙箱）",
		MaxTurns: loaded.Config.Context.MaxTurns,
		Terminal: doctorTerminal{
			TTY: options.interactive, Color: options.interactive && !environmentIsSet(a.lookupEnv, "NO_COLOR"),
			InteractiveInput: doctorInteractiveInput(a.stdin, options.interactive),
		},
	}
	report.addCheck("platform", "pass", "平台 "+report.Platform+"，Go "+report.GoVersion)
	report.addConfigChecks()
	a.addProjectChecks(ctx, loaded, &report)
	a.addLocalToolChecks(loaded, &report)
	a.addProviderChecks(ctx, profile, options, &report)
	report.Status = summarizeDoctorStatus(report.Checks)
	return report
}

func (report *doctorReport) addCheck(id, status, summary string) {
	report.Checks = append(report.Checks, doctorCheck{ID: id, Status: status, Summary: summary})
}

func (report *doctorReport) addConfigChecks() {
	if report.UserConfigLoaded || report.ProjectConfigLoaded {
		report.addCheck("configuration", "pass", "配置已解析，当前 profile 为 "+report.Profile)
	} else {
		report.addCheck("configuration", "warn", "未找到配置文件，当前使用内置默认配置")
	}
	if report.Provider == "" || report.Model == "" {
		report.addCheck("provider_configuration", "warn", "Provider 或模型尚未配置")
	} else {
		report.addCheck("provider_configuration", "pass", report.Provider+":"+report.Model)
	}
	if report.APIKeyEnvironment == "" {
		report.addCheck("credential", "warn", "未配置 API Key 环境变量名；仅无鉴权端点可用")
	} else if report.CredentialSet {
		report.addCheck("credential", "pass", report.APIKeyEnvironment+" 已设置（不显示值）")
	} else {
		report.addCheck("credential", "fail", report.APIKeyEnvironment+" 未设置")
	}
}

func (a *App) addProjectChecks(ctx context.Context, loaded config.Loaded, report *doctorReport) {
	instructions, err := project.LoadAgents(project.AgentsOptions{
		UserConfigDir: filepath.Dir(loaded.UserConfigPath), ProjectRoot: loaded.ProjectRoot, WorkDir: loaded.WorkingDir,
	})
	if err != nil {
		report.addCheck("agents", "fail", "AGENTS.md 加载失败（检查文件类型、大小、UTF-8 和项目边界）")
	} else {
		for _, instruction := range instructions {
			report.Agents = append(report.Agents, doctorPath(instruction.Path, loaded))
		}
		if len(report.Agents) == 0 {
			report.addCheck("agents", "pass", "未发现 AGENTS.md")
		} else {
			report.addCheck("agents", "pass", fmt.Sprintf("按从宽到近顺序加载 %d 个 AGENTS.md", len(report.Agents)))
		}
	}
	catalog, err := skills.Discover(skills.Options{
		UserHomeDir: a.userHomeDir, ProjectRoot: loaded.ProjectRoot,
	})
	if err != nil {
		report.addCheck("skills", "fail", "Skills 加载失败（检查目录边界、文件类型、大小、UTF-8 和 frontmatter）")
	} else {
		for _, skill := range catalog.Skills {
			report.Skills = append(report.Skills, doctorSkill{
				Name: skill.Name, Description: skill.Description, Source: skill.Source, Scope: string(skill.Scope),
			})
		}
		for _, conflict := range catalog.Conflicts {
			report.SkillConflicts = append(report.SkillConflicts, doctorSkillConflict{
				Name: conflict.Name, WinnerSource: conflict.WinnerSource, IgnoredSource: conflict.IgnoredSource,
			})
		}
		switch {
		case len(report.SkillConflicts) > 0:
			report.addCheck("skills", "warn", fmt.Sprintf("发现 %d 个 Skill；%d 个同名冲突按项目级优先处理", len(report.Skills), len(report.SkillConflicts)))
		case len(report.Skills) > 0:
			report.addCheck("skills", "pass", fmt.Sprintf("发现 %d 个按需加载的本地 Skill", len(report.Skills)))
		default:
			report.addCheck("skills", "pass", "未发现本地 Skill")
		}
	}
	report.Git.Repository = gitMarkerExists(loaded.ProjectRoot)
	gitPath, lookErr := exec.LookPath("git")
	if lookErr == nil {
		report.Git.Executable = doctorPath(gitPath, loaded)
	}
	if !report.Git.Repository {
		report.addCheck("git", "warn", "项目根未发现 .git；仍可运行，但边界仅锚定当前目录")
		return
	}
	if lookErr != nil {
		report.addCheck("git", "warn", "已发现 Git 仓库，但 PATH 中没有 git")
		return
	}
	tracked, err := doctorTrackedChanges(ctx, gitPath, loaded.ProjectRoot, a.environment())
	if err != nil {
		report.addCheck("git", "warn", "Git 状态检查失败："+boundedDiagnostic(err.Error()))
		return
	}
	report.Git.TrackedChanges = &tracked
	if tracked {
		report.addCheck("git", "pass", "Git 仓库可用，存在未提交的已跟踪修改")
	} else {
		report.addCheck("git", "pass", "Git 仓库可用，已跟踪文件干净")
	}
}

func (a *App) addLocalToolChecks(loaded config.Loaded, report *doctorReport) {
	shell, err := platform.ResolveShell(a.environment())
	if err != nil {
		report.addCheck("shell", "warn", "未找到受支持的非交互 shell："+boundedDiagnostic(err.Error()))
	} else {
		report.Shell = doctorShell{Available: true, Name: shell.Name, Executable: doctorPath(shell.Executable, loaded)}
		report.addCheck("shell", "pass", shell.Name+" · "+report.Shell.Executable)
	}
	rgPath, err := exec.LookPath("rg")
	if err != nil {
		report.Ripgrep.Fallback = "Go walker"
		report.addCheck("ripgrep", "warn", "未找到 rg，将使用较慢的 Go fallback")
	} else {
		report.Ripgrep = doctorTool{Available: true, Executable: doctorPath(rgPath, loaded), Fallback: "Go walker"}
		report.addCheck("ripgrep", "pass", "rg 可用；失败时仍可退化为 Go walker")
	}
	if report.Terminal.TTY && report.Terminal.InteractiveInput {
		report.addCheck("terminal", "pass", "TTY 可用，支持交互审批")
	} else if report.Terminal.TTY {
		report.addCheck("terminal", "warn", "输出为 TTY，但标准输入不可交互；审批将被禁用")
	} else {
		report.addCheck("terminal", "pass", "当前输出非 TTY；交互审批将被禁用")
	}
}

func (a *App) addProviderChecks(ctx context.Context, profile config.Profile, options doctorOptions, report *doctorReport) {
	if options.offline {
		report.ProviderProbe = doctorProviderProbe{Status: "skipped"}
		report.addCheck("provider_probe", "skip", "离线模式：未连接 Provider")
		return
	}
	if profile.Provider == "" || profile.Model == "" {
		report.ProviderProbe = doctorProviderProbe{Status: "skipped"}
		report.addCheck("provider_probe", "skip", "Provider 未配置，未执行在线探测")
		return
	}
	if profile.APIKeyEnv != "" && !report.CredentialSet {
		report.ProviderProbe = doctorProviderProbe{Status: "failed", Category: string(provider.ErrorAuthentication), Code: "credential_missing"}
		report.addCheck("provider_probe", "fail", "凭据缺失，未发送网络请求")
		return
	}
	apiKey := ""
	if profile.APIKeyEnv != "" {
		apiKey, _ = a.lookupEnv(profile.APIKeyEnv)
	}
	modelProvider, err := a.newProvider(profile, apiKey)
	if err != nil {
		report.ProviderProbe = doctorProviderProbe{Status: "failed", Category: string(provider.ErrorInvalidRequest), Code: "provider_setup"}
		report.addCheck("provider_probe", "fail", "Provider 初始化失败（检查 provider、base_url 与超时配置）")
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, options.probeTimeout)
	defer cancel()
	capabilities, err := modelProvider.Capabilities(probeCtx, profile.Model)
	if err != nil {
		a.recordProbeError(report, err, 0)
		return
	}
	report.Capabilities = &doctorCapabilities{
		ToolCalling: capabilities.ToolCalling, ParallelTools: capabilities.ParallelTools,
		UsageInStream: capabilities.UsageInStream, StrictToolSchema: capabilities.StrictToolSchema,
		MaxContextTokens: capabilities.MaxContextTokens,
	}
	started := a.now()
	response, err := modelProvider.Stream(probeCtx, doctorProbeRequest(profile.Model, capabilities.ToolCalling), doctorProbeSink{})
	duration := a.now().Sub(started)
	if err != nil {
		a.recordProbeError(report, err, duration)
		return
	}
	probe := doctorProviderProbe{Attempted: true, Status: "passed", DurationMS: duration.Milliseconds()}
	if capabilities.ToolCalling {
		probe.ToolCalling = validDoctorToolCall(response)
		if !probe.ToolCalling {
			probe.Status, probe.Category, probe.Code = "failed", "capability", "tool_call_not_observed"
			report.ProviderProbe = probe
			report.addCheck("provider_probe", "fail", "端点与认证可用，但模型未按要求产生工具调用")
			return
		}
	}
	report.ProviderProbe = probe
	if capabilities.ToolCalling {
		report.addCheck("provider_probe", "pass", "端点、认证、SSE 与工具调用探测通过")
	} else {
		report.addCheck("provider_probe", "fail", "端点可用，但所选模型声明不支持工具调用")
		report.ProviderProbe.Status, report.ProviderProbe.Category, report.ProviderProbe.Code = "failed", "capability", "tool_calling_unsupported"
	}
}

func (a *App) recordProbeError(report *doctorReport, err error, duration time.Duration) {
	probe := doctorProviderProbe{Attempted: true, Status: "failed", DurationMS: duration.Milliseconds()}
	if providerErr, ok := provider.AsError(err); ok {
		probe.Category, probe.Code = string(providerErr.Category), providerErr.Code
	} else if errors.Is(err, context.DeadlineExceeded) {
		probe.Category, probe.Code = string(provider.ErrorTimeout), "deadline_exceeded"
	} else if errors.Is(err, context.Canceled) {
		probe.Category, probe.Code = string(provider.ErrorCanceled), "canceled"
	} else {
		probe.Category, probe.Code = "runtime", "probe_failed"
	}
	report.ProviderProbe = probe
	report.addCheck("provider_probe", "fail", fmt.Sprintf("Provider 探测失败：%s/%s", probe.Category, probe.Code))
}

func doctorProbeRequest(model string, toolCalling bool) provider.ChatRequest {
	request := provider.ChatRequest{
		Model: model,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "你正在执行 MengDie Code 固定诊断。不得读取文件、执行命令或输出隐藏推理。"},
			{Role: provider.RoleUser, Content: fmt.Sprintf("如果提供了 doctor_echo 工具，请只调用它一次并传入 value=%s；否则只回复 %s。", doctorProbeToken, doctorProbeToken)},
		},
		ToolChoice: provider.ToolChoiceNone,
	}
	if toolCalling {
		request.Tools = []provider.Tool{{Type: "function", Function: provider.FunctionDefinition{
			Name: doctorProbeTool, Description: "返回固定诊断值，不产生任何副作用",
			Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
		}}}
		request.ToolChoice = provider.ToolChoiceRequired
	}
	return request
}

func validDoctorToolCall(response *provider.ChatResponse) bool {
	if response == nil {
		return false
	}
	for _, call := range response.Message.ToolCalls {
		if call.Name != doctorProbeTool || !json.Valid(call.Arguments) {
			continue
		}
		var arguments struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(call.Arguments, &arguments) == nil && arguments.Value == doctorProbeToken {
			return true
		}
	}
	return false
}

type doctorProbeSink struct{}

func (doctorProbeSink) OnEvent(context.Context, provider.StreamEvent) error { return nil }

func doctorExitCode(report doctorReport) int {
	if report.ProviderProbe.Status == "failed" {
		return ExitProviderError
	}
	for _, check := range report.Checks {
		if check.Status == "fail" {
			return ExitRunError
		}
	}
	return ExitOK
}

func summarizeDoctorStatus(checks []doctorCheck) string {
	status := "ok"
	for _, check := range checks {
		if check.Status == "fail" {
			return "error"
		}
		if check.Status == "warn" {
			status = "warning"
		}
	}
	return status
}

func (a *App) writeDoctorReport(report doctorReport) error {
	if _, err := fmt.Fprintf(a.stdout, "MengDie Code Doctor · schema %d · %s\n", report.SchemaVersion, report.Status); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "版本 %s · %s · %s\n项目 %s · %s\n安全 %s\n",
		report.Version, report.Platform, report.GoVersion, report.ProjectName, report.ProjectRoot, report.Security); err != nil {
		return err
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(a.stdout, "%s %s\n", doctorMark(check.Status), check.Summary); err != nil {
			return err
		}
	}
	if len(report.Agents) > 0 {
		if _, err := fmt.Fprintln(a.stdout, "AGENTS.md 生效链："); err != nil {
			return err
		}
		for _, path := range report.Agents {
			if _, err := fmt.Fprintln(a.stdout, "  - "+path); err != nil {
				return err
			}
		}
	}
	if len(report.Skills) > 0 {
		if _, err := fmt.Fprintln(a.stdout, "Skill catalog（全文按需加载）："); err != nil {
			return err
		}
		for _, skill := range report.Skills {
			if _, err := fmt.Fprintf(a.stdout, "  - %s · %s · %s\n", skill.Name, skill.Scope, skill.Source); err != nil {
				return err
			}
		}
	}
	if len(report.SkillConflicts) > 0 {
		if _, err := fmt.Fprintln(a.stdout, "Skill 同名冲突（项目级优先）："); err != nil {
			return err
		}
		for _, conflict := range report.SkillConflicts {
			if _, err := fmt.Fprintf(a.stdout, "  - %s：使用 %s，忽略 %s\n", conflict.Name, conflict.WinnerSource, conflict.IgnoredSource); err != nil {
				return err
			}
		}
	}
	return nil
}

func doctorMark(status string) string {
	switch status {
	case "pass":
		return "✓"
	case "fail":
		return "✗"
	case "warn":
		return "!"
	default:
		return "•"
	}
}

func doctorPath(path string, loaded config.Loaded) string {
	clean := filepath.Clean(path)
	if relative, ok := relativeWithin(loaded.ProjectRoot, clean); ok {
		if relative == "." {
			return "$PROJECT_ROOT"
		}
		return filepath.Join("$PROJECT_ROOT", relative)
	}
	userRoot := filepath.Dir(loaded.UserConfigPath)
	if relative, ok := relativeWithin(userRoot, clean); ok {
		if relative == "." {
			return "$USER_CONFIG"
		}
		return filepath.Join("$USER_CONFIG", relative)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if relative, ok := relativeWithin(home, clean); ok {
			if relative == "." {
				return "~"
			}
			return filepath.Join("~", relative)
		}
	}
	return clean
}

func relativeWithin(root, path string) (string, bool) {
	resolvedRoot, rootErr := filepath.EvalSymlinks(filepath.Clean(root))
	resolvedPath, pathErr := filepath.EvalSymlinks(filepath.Clean(path))
	if rootErr == nil && pathErr == nil {
		return lexicalRelativeWithin(resolvedRoot, resolvedPath)
	}
	return lexicalRelativeWithin(root, path)
}

func lexicalRelativeWithin(root, path string) (string, bool) {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

func providerOrigin(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func environmentIsSet(lookup func(string) (string, bool), name string) bool {
	_, exists := lookup(name)
	return exists
}

func doctorInteractiveInput(reader io.Reader, outputInteractive bool) bool {
	if !outputInteractive {
		return false
	}
	file, ok := reader.(*os.File)
	if !ok {
		return true
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func gitMarkerExists(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

func doctorTrackedChanges(ctx context.Context, gitPath, root string, environment []string) (bool, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, args := range [][]string{
		{"-c", "core.fsmonitor=false", "-C", root, "diff", "--quiet", "--no-ext-diff", "--no-textconv"},
		{"-c", "core.fsmonitor=false", "-C", root, "diff", "--cached", "--quiet", "--no-ext-diff", "--no-textconv"},
	} {
		command := exec.CommandContext(checkCtx, gitPath, args...)
		command.Env = doctorGitEnvironment(environment)
		err := command.Run()
		if err == nil {
			continue
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func doctorGitEnvironment(environment []string) []string {
	allowed := map[string]struct{}{
		"comspec": {}, "home": {}, "lang": {}, "lc_all": {}, "lc_ctype": {},
		"path": {}, "pathext": {}, "systemroot": {}, "temp": {}, "tmp": {},
		"tmpdir": {}, "userprofile": {}, "windir": {},
	}
	filtered := make([]string, 0, len(allowed)+2)
	seen := make(map[string]struct{}, len(allowed))
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := allowed[key]; !ok {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		filtered = append(filtered, entry)
		seen[key] = struct{}{}
	}
	return append(filtered, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
}

func boundedDiagnostic(message string) string {
	message = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(message, "\r", " "), "\n", " "))
	const limit = 240
	if len(message) <= limit {
		return message
	}
	return message[:limit] + "…"
}
