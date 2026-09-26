// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package evaluation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

const realDiagnosticTimeout = 90 * time.Minute

type RealAgentRunOptions struct {
	Workspace       string
	StateDir        string
	Prompt          string
	Budget          RealRepositoryRiskBudget
	CommandPrefixes []string
	AllowEdit       bool
	AllowedChanges  []string
}

type RealAgentRunEvidence struct {
	Status                string `json:"status"`
	ToolCalls             int    `json:"tool_calls"`
	CommandCalls          int    `json:"command_calls"`
	DeniedTools           int    `json:"denied_tools"`
	ForcedCleanupCount    int    `json:"forced_cleanup_count"`
	UncertainExecuteCount int    `json:"uncertain_execute_count"`
	InputTokens           int64  `json:"input_tokens"`
	OutputTokens          int64  `json:"output_tokens"`
}

type RealAgentRunner func(context.Context, RealAgentRunOptions) (RealAgentRunEvidence, error)

type RealRepositoryRunOptions struct {
	VerifierExecutable string
	CommandPrefixes    []string
	AllowAgentEdit     bool
	Agent              RealAgentRunner
}

type RealVerifierEvidence struct {
	Status          string `json:"status"`
	ExitCode        int    `json:"exit_code"`
	DurationMillis  int64  `json:"duration_ms"`
	StdoutSHA256    string `json:"stdout_sha256"`
	StderrSHA256    string `json:"stderr_sha256"`
	OutputTruncated bool   `json:"output_truncated"`
	ForcedCleanup   bool   `json:"forced_cleanup"`
}

// RealRepositoryRunResult never contains prompts, source, raw output or keys.
// diagnostic_passed means the local checks passed; it is not an M1 score.
type RealRepositoryRunResult struct {
	SchemaVersion      int                   `json:"schema_version"`
	RunID              string                `json:"run_id"`
	Mode               string                `json:"mode"`
	Status             string                `json:"status"`
	ManifestID         string                `json:"manifest_id"`
	ManifestSHA256     string                `json:"manifest_sha256"`
	TaskID             string                `json:"task_id"`
	SourceCommit       string                `json:"source_commit"`
	VerifierSHA256     string                `json:"verifier_sha256"`
	Platform           string                `json:"platform"`
	StartedAt          time.Time             `json:"started_at"`
	DurationMillis     int64                 `json:"duration_ms"`
	Baseline           *RealVerifierEvidence `json:"baseline,omitempty"`
	Agent              *RealAgentRunEvidence `json:"agent,omitempty"`
	Final              *RealVerifierEvidence `json:"final,omitempty"`
	ChangedFiles       []RealChangedFile     `json:"changed_files,omitempty"`
	OutsideSideEffects string                `json:"outside_side_effects"`
	CleanupFailed      bool                  `json:"cleanup_failed"`
}

type realDiagnosticDeps struct {
	prepare  realBaselinePrepare
	baseline func(context.Context, RealRepositoryManifest, string, RealRepositoryBaselineOptions) (RealRepositoryBaselineResult, error)
	process  realBaselineProcess
}

// RunRealRepositoryDiagnostic runs a single local, unisolated task. The
// caller must explicitly opt in before invoking it on an untrusted source.
func RunRealRepositoryDiagnostic(ctx context.Context, manifest RealRepositoryManifest, taskID string, options RealRepositoryRunOptions) (RealRepositoryRunResult, error) {
	return runRealRepositoryDiagnostic(ctx, manifest, taskID, options, realDiagnosticDeps{
		prepare: PrepareRealRepositoryTask, baseline: RunRealRepositoryBaseline, process: platform.RunProcess,
	})
}

func runRealRepositoryDiagnostic(ctx context.Context, manifest RealRepositoryManifest, taskID string, options RealRepositoryRunOptions, deps realDiagnosticDeps) (result RealRepositoryRunResult, err error) {
	if ctx == nil || options.Agent == nil || deps.prepare == nil || deps.baseline == nil || deps.process == nil {
		return result, errors.New("real repository diagnostic requires execution dependencies")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := manifest.Validate(); err != nil {
		return result, errors.New("invalid real repository manifest")
	}
	task, ok := manifestTaskByID(manifest, taskID)
	if !ok {
		return result, errors.New("real repository task not found")
	}
	if task.RiskBudget.NetworkAccess == "allowlisted" {
		return result, errors.New("allowlisted network is unsupported in local diagnostics")
	}
	executable, err := resolveRealBaselineExecutable(options.VerifierExecutable, task.Verifier.Command[0])
	if err != nil {
		return result, errors.New("invalid verifier executable")
	}
	verifierHash, err := hashRealBaselineExecutable(executable)
	if err != nil {
		return result, errors.New("cannot hash verifier executable")
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return result, errors.New("cannot encode real repository manifest")
	}
	manifestHash := sha256.Sum256(manifestBytes)
	var runID [16]byte
	if _, err := rand.Read(runID[:]); err != nil {
		return result, errors.New("cannot generate diagnostic run id")
	}
	started := time.Now().UTC()
	result = RealRepositoryRunResult{
		SchemaVersion: 1, RunID: hex.EncodeToString(runID[:]), Mode: "local_unisolated", Status: "preflight_failed",
		ManifestID: manifest.ID, ManifestSHA256: hex.EncodeToString(manifestHash[:]), TaskID: task.ID,
		SourceCommit: task.SourceCommit, VerifierSHA256: verifierHash, Platform: runtime.GOOS + "/" + runtime.GOARCH,
		StartedAt: started, OutsideSideEffects: "unverified",
	}
	defer func() { result.DurationMillis = time.Since(started).Milliseconds() }()
	deadline, cancel := context.WithTimeout(ctx, realDiagnosticTimeout)
	defer cancel()
	var workspaces []*PreparedRealRepository
	defer func() {
		for _, workspace := range workspaces {
			if closeErr := workspace.Close(); closeErr != nil {
				result.CleanupFailed = true
				result.Status = "indeterminate"
			}
		}
	}()

	baseline, baselineErr := deps.baseline(deadline, manifest, taskID, RealRepositoryBaselineOptions{VerifierExecutable: executable})
	if baselineErr != nil {
		return result, nil
	}
	result.Baseline = realVerifierEvidence(baseline)
	if baseline.VerifierSHA256 != verifierHash || baseline.SourceCommit != task.SourceCommit {
		result.Status = "preflight_failed"
		return result, nil
	}
	if baseline.Status != "baseline_matched" {
		switch baseline.Status {
		case "baseline_mismatch":
			result.Status = "baseline_mismatch"
		case "cancelled", "timeout":
			result.Status = realDiagnosticContextStatus(deadline, baseline.Status)
		case "preflight_failed", "source_prepare_failed":
			result.Status = "preflight_failed"
		default:
			result.Status = "indeterminate"
		}
		return result, nil
	}

	working, prepareErr := deps.prepare(deadline, manifest, taskID)
	if working != nil {
		workspaces = append(workspaces, working)
	}
	if prepareErr != nil || working == nil || working.Path == "" || working.SourceCommit != task.SourceCommit {
		result.Status = realDiagnosticContextStatus(deadline, "source_prepare_failed")
		return result, nil
	}
	before, snapshotErr := captureRealSnapshot(working.Path)
	if snapshotErr != nil {
		result.Status = "unsupported"
		return result, nil
	}
	stateDir, stateErr := os.MkdirTemp("", "mengdie-real-agent-state-")
	if stateErr != nil {
		return result, nil
	}
	defer func() {
		if removeErr := os.RemoveAll(stateDir); removeErr != nil {
			result.CleanupFailed = true
			result.Status = "indeterminate"
		}
	}()
	agentEvidence, agentErr := options.Agent(deadline, RealAgentRunOptions{
		Workspace: working.Path, StateDir: stateDir, Prompt: task.Prompt,
		Budget: task.RiskBudget, CommandPrefixes: append([]string(nil), options.CommandPrefixes...),
		AllowEdit:      options.AllowAgentEdit,
		AllowedChanges: append([]string(nil), task.Acceptance.AllowedChanges...),
	})
	result.Agent = &agentEvidence
	if agentEvidence.ToolCalls < 0 || agentEvidence.CommandCalls < 0 || agentEvidence.ToolCalls > task.RiskBudget.MaxToolCalls || agentEvidence.CommandCalls > task.RiskBudget.MaxCommandCalls {
		result.Status = "indeterminate"
		return result, nil
	}
	if agentEvidence.DeniedTools > 0 && agentEvidence.Status == "completed" {
		result.Status = "policy_denied"
		return result, nil
	}
	if agentEvidence.ForcedCleanupCount > 0 || agentEvidence.UncertainExecuteCount > 0 {
		result.Status = "indeterminate"
		return result, nil
	}
	if agentErr != nil || agentEvidence.Status != "completed" {
		result.Status = realDiagnosticContextStatus(deadline, "agent_failed")
		if agentEvidence.Status == "policy_denied" || agentEvidence.Status == "budget_exhausted" {
			result.Status = agentEvidence.Status
		} else if agentEvidence.Status == "indeterminate" {
			result.Status = "indeterminate"
		}
		return result, nil
	}
	if deadline.Err() != nil {
		result.Status = realDiagnosticContextStatus(deadline, "agent_failed")
		return result, nil
	}
	after, snapshotErr := captureRealSnapshot(working.Path)
	if snapshotErr != nil {
		result.Status = "indeterminate"
		return result, nil
	}
	changes, diffErr := allowedRealChanges(before, after, task.Acceptance.AllowedChanges)
	if diffErr != nil {
		result.Status = "policy_violation"
		return result, nil
	}
	result.ChangedFiles = changes
	finalWorkspace, prepareErr := deps.prepare(deadline, manifest, taskID)
	if finalWorkspace != nil {
		workspaces = append(workspaces, finalWorkspace)
	}
	if prepareErr != nil || finalWorkspace == nil || finalWorkspace.Path == "" || finalWorkspace.SourceCommit != task.SourceCommit {
		result.Status = realDiagnosticContextStatus(deadline, "source_prepare_failed")
		return result, nil
	}
	if err := rebuildRealChanges(working.Path, finalWorkspace.Path, before, after, changes); err != nil {
		result.Status = "indeterminate"
		return result, nil
	}
	currentHash, hashErr := hashRealBaselineExecutable(executable)
	if hashErr != nil || currentHash != verifierHash {
		result.Status = "preflight_failed"
		return result, nil
	}
	finalManifest := manifest
	finalManifest.Tasks = append([]RealRepositoryTask(nil), manifest.Tasks...)
	for index := range finalManifest.Tasks {
		if finalManifest.Tasks[index].ID == taskID {
			finalManifest.Tasks[index].Baseline.ExpectedExitCode = 0
		}
	}
	final, finalErr := runRealRepositoryBaseline(deadline, finalManifest, taskID,
		RealRepositoryBaselineOptions{VerifierExecutable: executable},
		func(context.Context, RealRepositoryManifest, string) (*PreparedRealRepository, error) {
			return finalWorkspace, nil
		},
		deps.process, func(*PreparedRealRepository) error { return nil })
	if finalErr != nil {
		result.Status = "verifier_failed"
		return result, nil
	}
	result.Final = realVerifierEvidence(final)
	if final.VerifierSHA256 != verifierHash || final.Status != "baseline_matched" {
		result.Status = realDiagnosticContextStatus(deadline, "verifier_failed")
		if final.Status == "indeterminate" || final.Status == "cleanup_failed" {
			result.Status = "indeterminate"
		}
		return result, nil
	}
	if len(changes) == 0 {
		result.Status = "indeterminate"
		return result, nil
	}
	result.Status = "diagnostic_passed"
	return result, nil
}

func realVerifierEvidence(baseline RealRepositoryBaselineResult) *RealVerifierEvidence {
	return &RealVerifierEvidence{Status: baseline.Status, ExitCode: baseline.ActualExitCode,
		DurationMillis: baseline.VerifierDurationMillis, StdoutSHA256: baseline.StdoutSHA256,
		StderrSHA256: baseline.StderrSHA256, OutputTruncated: baseline.OutputTruncated,
		ForcedCleanup: baseline.ForcedCleanup}
}

func realDiagnosticContextStatus(ctx context.Context, fallback string) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return "cancelled"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	return fallback
}
