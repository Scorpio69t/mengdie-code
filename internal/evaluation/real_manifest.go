// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// RealRepositoryManifest declares reviewable external repository tasks. Loading
// or validating this manifest never clones a repository or executes a command.
type RealRepositoryManifest struct {
	SchemaVersion int                  `json:"schema_version"`
	ID            string               `json:"id"`
	Description   string               `json:"description"`
	Tasks         []RealRepositoryTask `json:"tasks"`
}

// RealRepositoryTask pins a public source revision and its evaluation contract.
type RealRepositoryTask struct {
	ID           string                   `json:"id"`
	Title        string                   `json:"title"`
	SourceURL    string                   `json:"source_url"`
	SourceCommit string                   `json:"source_commit"`
	License      string                   `json:"license"`
	TaskSource   string                   `json:"task_source"`
	Prompt       string                   `json:"prompt"`
	Verifier     VerifySpec               `json:"verifier"`
	Baseline     BaselineSpec             `json:"baseline"`
	Acceptance   RealRepositoryAcceptance `json:"acceptance"`
	RiskBudget   RealRepositoryRiskBudget `json:"risk_budget"`
}

// RealRepositoryAcceptance limits changed paths to explicit workspace-relative
// file names. Wildcards are intentionally not supported in schema v1.
type RealRepositoryAcceptance struct {
	AllowedChanges []string `json:"allowed_changes"`
}

// RealRepositoryRiskBudget bounds the effects an Agent may request for a task.
type RealRepositoryRiskBudget struct {
	AllowedEffects  []string `json:"allowed_effects"`
	NetworkAccess   string   `json:"network_access"`
	MaxToolCalls    int      `json:"max_tool_calls"`
	MaxCommandCalls int      `json:"max_command_calls"`
}

// RealRepositoryManifestSummary omits prompts and verifier arguments so
// validation output can be shared without disclosing task text or command data.
type RealRepositoryManifestSummary struct {
	SchemaVersion int                         `json:"schema_version"`
	ID            string                      `json:"id"`
	TaskCount     int                         `json:"task_count"`
	Tasks         []RealRepositoryTaskSummary `json:"tasks"`
}

type RealRepositoryTaskSummary struct {
	ID                 string `json:"id"`
	SourceCommit       string `json:"source_commit"`
	License            string `json:"license"`
	AllowedChangeCount int    `json:"allowed_change_count"`
	VerifierArgCount   int    `json:"verifier_arg_count"`
	ValidationStatus   string `json:"validation_status"`
}

var fullGitCommitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var spdxIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+-]*$`)
var manifestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// LoadRealRepositoryManifest reads a strict JSON manifest and validates it.
func LoadRealRepositoryManifest(path string) (manifest RealRepositoryManifest, err error) {
	file, err := os.Open(path)
	if err != nil {
		return RealRepositoryManifest{}, fmt.Errorf("open real repository manifest: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			manifest = RealRepositoryManifest{}
			err = errors.Join(err, fmt.Errorf("close real repository manifest: %w", closeErr))
		}
	}()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return RealRepositoryManifest{}, fmt.Errorf("decode real repository manifest: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return RealRepositoryManifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return RealRepositoryManifest{}, err
	}
	return manifest, nil
}

// Validate checks the task contract without network or filesystem side effects.
func (m RealRepositoryManifest) Validate() error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("unsupported real repository schema_version %d, want 1", m.SchemaVersion)
	}
	if !manifestIDPattern.MatchString(m.ID) {
		return errors.New("real repository manifest id must be a short slug")
	}
	if strings.TrimSpace(m.Description) == "" {
		return errors.New("real repository manifest description is required")
	}
	if len(m.Tasks) == 0 {
		return errors.New("real repository manifest must contain at least one task")
	}
	seen := make(map[string]struct{}, len(m.Tasks))
	for index, task := range m.Tasks {
		if err := task.validate(); err != nil {
			return fmt.Errorf("task %d: %w", index, err)
		}
		if _, exists := seen[task.ID]; exists {
			return fmt.Errorf("duplicate real repository task id %q", task.ID)
		}
		seen[task.ID] = struct{}{}
	}
	return nil
}

func (t RealRepositoryTask) validate() error {
	if !manifestIDPattern.MatchString(t.ID) {
		return errors.New("id must be a short slug")
	}
	for name, value := range map[string]string{
		"title": t.Title, "license": t.License, "task_source": t.TaskSource, "prompt": t.Prompt,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("task %q %s is required", t.ID, name)
		}
	}
	if err := validatePublicGitURL(t.SourceURL); err != nil {
		return fmt.Errorf("task %q source_url: %w", t.ID, err)
	}
	if !fullGitCommitPattern.MatchString(t.SourceCommit) {
		return fmt.Errorf("task %q source_commit must be a full lowercase 40- or 64-character Git object id", t.ID)
	}
	if !spdxIdentifierPattern.MatchString(t.License) {
		return fmt.Errorf("task %q license must be an SPDX identifier", t.ID)
	}
	if len(t.Verifier.Command) == 0 {
		return fmt.Errorf("task %q verifier.command must be a non-empty argv array", t.ID)
	}
	for index, arg := range t.Verifier.Command {
		if strings.TrimSpace(arg) == "" || strings.ContainsAny(arg, "\r\n\x00") {
			return fmt.Errorf("task %q verifier.command[%d] must be a non-empty single-line argument", t.ID, index)
		}
	}
	if isShellExecutable(t.Verifier.Command[0]) {
		return fmt.Errorf("task %q verifier.command must invoke a verifier directly, not a shell", t.ID)
	}
	if strings.TrimSpace(t.Verifier.Timeout) == "" {
		return fmt.Errorf("task %q verifier.timeout is required", t.ID)
	}
	verifierTimeout, err := t.Verifier.duration()
	if err != nil {
		return fmt.Errorf("task %q verifier: %w", t.ID, err)
	}
	if verifierTimeout > 30*time.Minute {
		return fmt.Errorf("task %q verifier.timeout must not exceed 30m", t.ID)
	}
	if t.Baseline.ExpectedExitCode < 0 || t.Baseline.ExpectedExitCode > 255 {
		return fmt.Errorf("task %q baseline.expected_exit_code must be between 0 and 255", t.ID)
	}
	if len(t.Acceptance.AllowedChanges) == 0 || len(t.Acceptance.AllowedChanges) > 64 {
		return fmt.Errorf("task %q acceptance.allowed_changes must contain between 1 and 64 exact paths", t.ID)
	}
	seenChanges := make(map[string]struct{}, len(t.Acceptance.AllowedChanges))
	for _, path := range t.Acceptance.AllowedChanges {
		if strings.ContainsAny(path, "*?[]{}") || path == "." {
			return fmt.Errorf("task %q acceptance.allowed_changes must use exact, non-wildcard file paths", t.ID)
		}
		if err := validateWorkspaceRelativePath(path); err != nil {
			return fmt.Errorf("task %q acceptance.allowed_changes: %w", t.ID, err)
		}
		if _, exists := seenChanges[path]; exists {
			return fmt.Errorf("task %q acceptance.allowed_changes contains duplicate %q", t.ID, path)
		}
		seenChanges[path] = struct{}{}
	}
	if len(t.RiskBudget.AllowedEffects) == 0 {
		return fmt.Errorf("task %q risk_budget.allowed_effects is required", t.ID)
	}
	validEffects := map[string]bool{"read": true, "write": true, "execute": true, "network": true}
	seenEffects := make(map[string]struct{}, len(t.RiskBudget.AllowedEffects))
	for _, effect := range t.RiskBudget.AllowedEffects {
		if !validEffects[effect] {
			return fmt.Errorf("task %q risk_budget.allowed_effects contains unsupported effect %q", t.ID, effect)
		}
		if _, exists := seenEffects[effect]; exists {
			return fmt.Errorf("task %q risk_budget.allowed_effects contains duplicate %q", t.ID, effect)
		}
		seenEffects[effect] = struct{}{}
	}
	switch t.RiskBudget.NetworkAccess {
	case "disabled", "provider_only", "allowlisted":
	default:
		return fmt.Errorf("task %q risk_budget.network_access must be disabled, provider_only, or allowlisted", t.ID)
	}
	_, networkEffectAllowed := seenEffects["network"]
	if t.RiskBudget.NetworkAccess == "allowlisted" && !networkEffectAllowed {
		return fmt.Errorf("task %q allowlisted network access requires the network effect", t.ID)
	}
	if t.RiskBudget.NetworkAccess != "allowlisted" && networkEffectAllowed {
		return fmt.Errorf("task %q network effect requires allowlisted network access", t.ID)
	}
	if t.RiskBudget.MaxToolCalls < 1 || t.RiskBudget.MaxToolCalls > 10000 {
		return fmt.Errorf("task %q risk_budget.max_tool_calls must be between 1 and 10000", t.ID)
	}
	if t.RiskBudget.MaxCommandCalls < 0 || t.RiskBudget.MaxCommandCalls > t.RiskBudget.MaxToolCalls {
		return fmt.Errorf("task %q risk_budget.max_command_calls must be between 0 and max_tool_calls", t.ID)
	}
	return nil
}

func validatePublicGitURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return errors.New("must be an HTTPS public repository URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("must not include credentials, query parameters, or fragments")
	}
	if strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/" || strings.Contains(parsed.Path, "..") {
		return errors.New("must identify a repository path")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
		return errors.New("must use a public DNS host")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return errors.New("must not target a private or local address")
	}
	return nil
}

func isShellExecutable(value string) bool {
	base := value
	if separator := strings.LastIndexAny(base, `/\`); separator >= 0 {
		base = base[separator+1:]
	}
	base = strings.ToLower(base)
	switch base {
	case "sh", "sh.exe", "bash", "bash.exe", "zsh", "zsh.exe", "cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return true
	default:
		return false
	}
}

// Summary returns only non-prompt metadata; verifier argv and task prompt are
// deliberately omitted from command output.
func (m RealRepositoryManifest) Summary() RealRepositoryManifestSummary {
	summary := RealRepositoryManifestSummary{
		SchemaVersion: m.SchemaVersion,
		ID:            m.ID,
		TaskCount:     len(m.Tasks),
		Tasks:         make([]RealRepositoryTaskSummary, 0, len(m.Tasks)),
	}
	for _, task := range m.Tasks {
		summary.Tasks = append(summary.Tasks, RealRepositoryTaskSummary{
			ID:                 task.ID,
			SourceCommit:       task.SourceCommit,
			License:            task.License,
			AllowedChangeCount: len(task.Acceptance.AllowedChanges),
			VerifierArgCount:   len(task.Verifier.Command),
			ValidationStatus:   "valid",
		})
	}
	return summary
}

// EncodeRealRepositorySummary writes the redacted summary as JSON.
func EncodeRealRepositorySummary(writer io.Writer, manifest RealRepositoryManifest, pretty bool) error {
	encoder := json.NewEncoder(writer)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(manifest.Summary())
}
