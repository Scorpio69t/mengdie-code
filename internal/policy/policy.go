// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package policy evaluates deterministic tool authorization rules and issues
// one-shot capabilities. It is the only authority accepted at the tool
// execution boundary.
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

// Decision is the complete result of policy evaluation.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionDeny  Decision = "deny"
)

// Mode controls whether a human approval channel is available.
type Mode string

const (
	ModeInteractive Mode = "interactive"
	ModeHeadless    Mode = "headless"
)

var (
	ErrDenied          = errors.New("tool call denied by policy")
	ErrApprovalMissing = errors.New("approval broker is required")
	ErrReprepare       = errors.New("approval edited; call must be prepared again")
	ErrWorkDirMismatch = errors.New("authorization workdir does not match policy root")
)

// Rule matches a prepared call. Empty Tool and Effects are wildcards. When
// Sensitive is nil, path sensitivity is not part of the match.
type Rule struct {
	Name            string
	Tool            string
	Effects         []tools.Effect
	Sensitive       *bool
	CommandPrefixes []string
	Decision        Decision
}

// Options freezes the complete rule stack for a run. Slice order is
// significant: the first matching rule within a layer wins.
type Options struct {
	Root         string
	Mode         Mode
	CLI          []Rule
	Profile      []Rule
	ToolDefaults []Rule
}

// Result is safe to log: it contains no arguments, source text or diffs.
type Result struct {
	Decision Decision
	Reason   string
	Rule     string
}

// Engine applies hard invariants, then CLI, profile and tool-default rules.
type Engine struct {
	root         string
	mode         Mode
	cli          []Rule
	profile      []Rule
	toolDefaults []Rule
}

func NewEngine(options Options) (*Engine, error) {
	if options.Mode != ModeInteractive && options.Mode != ModeHeadless {
		return nil, fmt.Errorf("policy: unsupported mode %q", options.Mode)
	}
	root, err := canonicalDirectory(options.Root)
	if err != nil {
		return nil, fmt.Errorf("policy: project root: %w", err)
	}
	for _, layer := range [][]Rule{options.CLI, options.Profile, options.ToolDefaults} {
		for _, rule := range layer {
			if err := validateRule(rule); err != nil {
				return nil, err
			}
		}
	}
	return &Engine{
		root:         root,
		mode:         options.Mode,
		cli:          cloneRules(options.CLI),
		profile:      cloneRules(options.Profile),
		toolDefaults: cloneRules(options.ToolDefaults),
	}, nil
}

// canonicalDirectory resolves every filesystem alias before a path becomes a
// policy or capability boundary. In particular, macOS may expose /var through
// /private/var and Windows runners may use an alternate spelling for the same
// temporary directory.
func canonicalDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	canonical = filepath.Clean(canonical)
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return canonical, nil
}

func sameDirectory(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func validateRule(rule Rule) error {
	if rule.Decision != DecisionAllow && rule.Decision != DecisionAsk && rule.Decision != DecisionDeny {
		return fmt.Errorf("policy: rule %q has unsupported decision %q", rule.Name, rule.Decision)
	}
	for _, effect := range rule.Effects {
		if effect != tools.EffectRead && effect != tools.EffectWrite && effect != tools.EffectExecute && effect != tools.EffectNetwork && effect != tools.EffectState {
			return fmt.Errorf("policy: rule %q has unsupported effect %q", rule.Name, effect)
		}
	}
	if len(rule.CommandPrefixes) > 0 {
		if rule.Tool != "shell" || !hasEffect(rule.Effects, tools.EffectExecute) {
			return fmt.Errorf("policy: rule %q command prefixes require the shell execute effect", rule.Name)
		}
		for _, prefix := range rule.CommandPrefixes {
			if normalizeCommandPrefix(prefix) == "" || hasShellControlOperator(prefix) {
				return fmt.Errorf("policy: rule %q has unsafe command prefix %q", rule.Name, prefix)
			}
		}
	}
	return nil
}

func cloneRules(rules []Rule) []Rule {
	result := make([]Rule, len(rules))
	for index, rule := range rules {
		result[index] = rule
		result[index].Effects = append([]tools.Effect(nil), rule.Effects...)
		result[index].CommandPrefixes = append([]string(nil), rule.CommandPrefixes...)
	}
	return result
}

func (e *Engine) Mode() Mode { return e.mode }

// Evaluate is pure and deterministic for a frozen Engine and PreparedCall.
func (e *Engine) Evaluate(call *tools.PreparedCall) Result {
	if call == nil {
		return Result{Decision: DecisionDeny, Reason: "调用为空", Rule: "hard.invalid"}
	}
	if err := call.Validate(); err != nil {
		return Result{Decision: DecisionDeny, Reason: "调用协议无效", Rule: "hard.invalid"}
	}
	if result, denied := e.hardDeny(call); denied {
		return result
	}
	sensitive := hasSensitivePath(call)
	for _, layer := range []struct {
		name  string
		rules []Rule
	}{{"cli", e.cli}, {"profile", e.profile}, {"tool", e.toolDefaults}} {
		for _, rule := range layer.rules {
			if matches(rule, call, sensitive) {
				decision := rule.Decision
				if e.mode == ModeHeadless && decision == DecisionAsk {
					decision = DecisionDeny
				}
				name := rule.Name
				if name == "" {
					name = "unnamed"
				}
				return Result{Decision: decision, Reason: "命中授权规则", Rule: layer.name + "." + name}
			}
		}
	}

	if onlyEffect(call.Effects, tools.EffectRead) && !sensitive {
		return Result{Decision: DecisionAllow, Reason: "普通项目读取", Rule: "default.read"}
	}
	if onlyEffect(call.Effects, tools.EffectState) {
		return Result{Decision: DecisionAllow, Reason: "仅更新当前运行状态", Rule: "default.state"}
	}
	if e.mode == ModeHeadless {
		return Result{Decision: DecisionDeny, Reason: "无交互模式默认拒绝", Rule: "default.headless"}
	}
	return Result{Decision: DecisionAsk, Reason: "需要用户确认", Rule: "default.interactive"}
}

func (e *Engine) hardDeny(call *tools.PreparedCall) (Result, bool) {
	for _, effect := range call.Effects {
		if effect == tools.EffectNetwork {
			return Result{Decision: DecisionDeny, Reason: "M1 禁止网络工具", Rule: "hard.network"}, true
		}
	}
	for _, resource := range call.Paths {
		if !withinRoot(e.root, resource.Path) {
			return Result{Decision: DecisionDeny, Reason: "路径越出项目根", Rule: "hard.root"}, true
		}
		if resource.Sensitive && hasEffect(call.Effects, tools.EffectWrite) {
			return Result{Decision: DecisionDeny, Reason: "禁止写入受保护路径", Rule: "hard.protected_write"}, true
		}
		if e.mode == ModeHeadless && resource.Sensitive {
			return Result{Decision: DecisionDeny, Reason: "无交互模式禁止读取敏感路径", Rule: "hard.headless_sensitive"}, true
		}
	}
	return Result{}, false
}

func withinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func hasSensitivePath(call *tools.PreparedCall) bool {
	for _, resource := range call.Paths {
		if resource.Sensitive {
			return true
		}
	}
	return false
}

func hasEffect(effects []tools.Effect, wanted tools.Effect) bool {
	for _, effect := range effects {
		if effect == wanted {
			return true
		}
	}
	return false
}

func onlyEffect(effects []tools.Effect, wanted tools.Effect) bool {
	return len(effects) == 1 && effects[0] == wanted
}

func matches(rule Rule, call *tools.PreparedCall, sensitive bool) bool {
	if rule.Tool != "" && rule.Tool != call.ToolName {
		return false
	}
	if rule.Sensitive != nil && *rule.Sensitive != sensitive {
		return false
	}
	for _, wanted := range rule.Effects {
		if !hasEffect(call.Effects, wanted) {
			return false
		}
	}
	if len(rule.CommandPrefixes) > 0 && !matchesShellCommand(call, rule.CommandPrefixes) {
		return false
	}
	return true
}

func matchesShellCommand(call *tools.PreparedCall, prefixes []string) bool {
	if call.ToolName != "shell" {
		return false
	}
	var arguments struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(call.CanonicalArg, &arguments); err != nil || hasShellControlOperator(arguments.Command) {
		return false
	}
	command := normalizeCommandPrefix(arguments.Command)
	for _, raw := range prefixes {
		prefix := normalizeCommandPrefix(raw)
		if command == prefix || strings.HasPrefix(command, prefix+" ") {
			return true
		}
	}
	return false
}

func normalizeCommandPrefix(command string) string {
	return strings.Join(strings.Fields(command), " ")
}

func hasShellControlOperator(command string) bool {
	return strings.ContainsAny(command, "\r\n;&|<>`") || strings.Contains(command, "$(")
}
