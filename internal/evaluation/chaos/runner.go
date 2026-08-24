// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package chaos

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/agent"
	"github.com/Scorpio69t/mengdie-code/internal/cost"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/session"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

// Script names a deterministic provider script. Each script produces a fixed
// sequence of tool calls followed by a finish message so chaos runs are
// reproducible without depending on a real Provider.
const (
	ScriptEditThenFinish     = "edit_then_finish"
	ScriptReadThenFinish     = "read_then_finish"
	ScriptForceSummaryFinish = "force_summary_then_finish"
)

// ScenarioManifest mirrors the JSON shape in evals/chaos/all.json. Hook
// entries are parsed into the internal PlannedFire form so the runner never
// touches raw JSON after Load.
type ScenarioManifest struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	Description   string            `json:"description,omitempty"`
	FixtureRoot   string            `json:"fixture_root"`
	Tasks         []ScenarioTaskRaw `json:"tasks"`
}

// ScenarioTaskRaw is the JSON shape for one chaos scenario.
type ScenarioTaskRaw struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Fixture  string       `json:"fixture"`
	Script   string       `json:"script"`
	Verify   VerifySpec   `json:"verify"`
	Hooks    []HookSpec   `json:"hooks"`
	Recovery RecoverySpec `json:"recovery"`
	Tags     []string     `json:"tags,omitempty"`
}

// HookSpec mirrors chaos.PlannedFire; it is parsed once during Load.
type HookSpec struct {
	Hook     string `json:"hook"`
	Fire     string `json:"fire"`
	AfterSeq uint64 `json:"after_event_seq,omitempty"`
}

// VerifySpec is shared with internal/evaluation but re-declared to keep
// chaos runner free of an evaluation import cycle.
type VerifySpec struct {
	Command []string `json:"command"`
	Timeout string   `json:"timeout"`
}

// RecoverySpec declares the recovery contract that the runner verifies
// after each chaos round.
type RecoverySpec struct {
	ExpectedRecoveryKind         string `json:"expected_recovery_kind,omitempty"`
	ExpectedResumeCanResume      *bool  `json:"expected_resume_can_resume,omitempty"`
	ExpectedResumeReasonContains string `json:"expected_resume_reason_contains,omitempty"`
	ExpectedFinalExitCode        int    `json:"expected_final_exit_code"`
	ForbidDuplicateSideEffects   bool   `json:"forbid_duplicate_side_effects,omitempty"`
	ExpectedNoExtraProviderCalls bool   `json:"expected_no_extra_provider_calls,omitempty"`
	CommandIDDuplicate           bool   `json:"command_id_duplicate,omitempty"`
}

// Scenario is the loaded form of one task.
type Scenario struct {
	ID       string
	Title    string
	Fixture  string
	Script   string
	Verify   VerifySpec
	Hooks    []PlannedFire
	Recovery RecoverySpec
	Tags     []string
}

// LoadManifest reads a chaos manifest from disk and validates it.
func LoadManifest(path string) (ScenarioManifest, []Scenario, error) {
	file, err := os.Open(path)
	if err != nil {
		return ScenarioManifest{}, nil, fmt.Errorf("open chaos manifest: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var manifest ScenarioManifest
	if err := decoder.Decode(&manifest); err != nil {
		return ScenarioManifest{}, nil, fmt.Errorf("decode chaos manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return ScenarioManifest{}, nil, fmt.Errorf("unsupported chaos schema_version %d", manifest.SchemaVersion)
	}
	scenarios := make([]Scenario, 0, len(manifest.Tasks))
	for index, raw := range manifest.Tasks {
		scenario, err := convertScenario(raw)
		if err != nil {
			return ScenarioManifest{}, nil, fmt.Errorf("scenario %d: %w", index, err)
		}
		scenarios = append(scenarios, scenario)
	}
	return manifest, scenarios, nil
}

func convertScenario(raw ScenarioTaskRaw) (Scenario, error) {
	hooks := make([]PlannedFire, 0, len(raw.Hooks))
	for _, hookSpec := range raw.Hooks {
		planned, err := normalizePlannedFire(PlannedFire{
			Hook:     Hook(hookSpec.Hook),
			FireKind: FireKind(hookSpec.Fire),
			AfterSeq: hookSpec.AfterSeq,
		})
		if err != nil {
			return Scenario{}, err
		}
		hooks = append(hooks, planned)
	}
	return Scenario{
		ID:       strings.TrimSpace(raw.ID),
		Title:    raw.Title,
		Fixture:  strings.TrimSpace(raw.Fixture),
		Script:   raw.Script,
		Verify:   raw.Verify,
		Hooks:    hooks,
		Recovery: raw.Recovery,
		Tags:     raw.Tags,
	}, nil
}

// FixtureRoot resolves the manifest-relative fixture root.
func FixtureRoot(manifestPath, fixtureRoot string) (string, error) {
	abs, err := filepath.Abs(manifestPath)
	if err != nil {
		return "", fmt.Errorf("resolve manifest path: %w", err)
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(abs), filepath.FromSlash(fixtureRoot)))
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("stat fixture root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fixture root %q is not a directory", root)
	}
	return root, nil
}

// FixturePath resolves one fixture inside fixture_root.
func FixturePath(root, relative string) (string, error) {
	path := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("resolve fixture %q: %w", relative, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("fixture %q escapes fixture_root", relative)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat fixture %q: %w", relative, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fixture %q is not a directory", relative)
	}
	return path, nil
}

// Matrix is the deterministic, JSON-ready summary of one chaos run.
type Matrix struct {
	SchemaVersion  int              `json:"schema_version"`
	SuiteID        string           `json:"suite_id"`
	Mode           string           `json:"mode"`
	StartedAt      time.Time        `json:"started_at"`
	DurationMillis int64            `json:"duration_ms"`
	Passed         bool             `json:"passed"`
	Scenarios      []ScenarioMatrix `json:"scenarios"`
}

// ScenarioMatrix covers one scenario across one or more rounds.
type ScenarioMatrix struct {
	ID     string        `json:"id"`
	Title  string        `json:"title,omitempty"`
	Rounds []RoundResult `json:"rounds"`
	Passed bool          `json:"passed"`
	Reason string        `json:"reason,omitempty"`
}

// RoundResult describes the outcome of a single chaos round.
type RoundResult struct {
	Round                  int           `json:"round"`
	Seed                   int64         `json:"seed"`
	Observations           []Observation `json:"observations"`
	FirstRunError          string        `json:"first_run_error,omitempty"`
	ResumeCanResume        *bool         `json:"resume_can_resume,omitempty"`
	ResumeReason           string        `json:"resume_reason,omitempty"`
	ResumeRecoveryKind     string        `json:"resume_recovery_kind,omitempty"`
	ResumeRecoveryCallID   string        `json:"resume_recovery_call_id,omitempty"`
	SecondRunError         string        `json:"second_run_error,omitempty"`
	VerifyExitCode         int           `json:"verify_exit_code"`
	VerifyError            string        `json:"verify_error,omitempty"`
	DuplicateSideEffects   *bool         `json:"duplicate_side_effects,omitempty"`
	SideEffectHashBaseline string        `json:"side_effect_hash_baseline,omitempty"`
	SideEffectHashActual   string        `json:"side_effect_hash_actual,omitempty"`
	ExtraProviderCalls     *bool         `json:"extra_provider_calls,omitempty"`
	Passed                 bool          `json:"passed"`
	Reason                 string        `json:"reason,omitempty"`
}

// RunManifest executes each scenario for the requested number of rounds
// and returns the aggregated matrix.
func RunManifest(ctx context.Context, manifestPath string, rounds int, seed int64) (Matrix, error) {
	manifest, scenarios, err := LoadManifest(manifestPath)
	if err != nil {
		return Matrix{}, err
	}
	started := time.Now().UTC()
	matrix := Matrix{
		SchemaVersion: 1,
		SuiteID:       manifest.ID,
		Mode:          "chaos",
		StartedAt:     started,
		Passed:        true,
		Scenarios:     make([]ScenarioMatrix, 0, len(scenarios)),
	}
	fixtureRoot, err := FixtureRoot(manifestPath, manifest.FixtureRoot)
	if err != nil {
		return Matrix{}, err
	}
	for _, scenario := range scenarios {
		scenarioMatrix := ScenarioMatrix{ID: scenario.ID, Title: scenario.Title}
		for round := 0; round < rounds; round++ {
			result, err := RunRound(ctx, fixtureRoot, scenario, seed+int64(round))
			if err != nil {
				return Matrix{}, fmt.Errorf("scenario %s round %d: %w", scenario.ID, round, err)
			}
			scenarioMatrix.Rounds = append(scenarioMatrix.Rounds, result)
		}
		scenarioMatrix.Passed = allRoundsPassed(scenarioMatrix.Rounds)
		if !scenarioMatrix.Passed {
			matrix.Passed = false
			scenarioMatrix.Reason = summarizeRoundFailures(scenarioMatrix.Rounds)
		}
		matrix.Scenarios = append(matrix.Scenarios, scenarioMatrix)
	}
	matrix.DurationMillis = time.Since(started).Milliseconds()
	return matrix, nil
}

func allRoundsPassed(rounds []RoundResult) bool {
	for _, round := range rounds {
		if !round.Passed {
			return false
		}
	}
	return true
}

func summarizeRoundFailures(rounds []RoundResult) string {
	reasons := make([]string, 0)
	for _, round := range rounds {
		if round.Passed {
			continue
		}
		reasons = append(reasons, fmt.Sprintf("round %d: %s", round.Round, round.Reason))
	}
	return strings.Join(reasons, "; ")
}

// RunRound executes one chaos round end to end and returns the result.
func RunRound(ctx context.Context, fixtureRoot string, scenario Scenario, seed int64) (RoundResult, error) {
	result := RoundResult{Round: -1, Seed: seed}
	tempRoot, err := os.MkdirTemp("", "mengdie-chaos-*")
	if err != nil {
		return result, fmt.Errorf("create temp root: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempRoot) }()

	source, err := FixturePath(fixtureRoot, scenario.Fixture)
	if err != nil {
		return result, err
	}
	workspace := filepath.Join(tempRoot, "workspace")
	if err := copyTree(source, workspace); err != nil {
		return result, fmt.Errorf("copy fixture: %w", err)
	}

	ctrl, err := New(Schedule{Seed: seed, Fires: scenario.Hooks})
	if err != nil {
		return result, fmt.Errorf("build chaos controller: %w", err)
	}
	rec := &scriptedRecorder{}

	store, sessionID, err := openSessionStore(ctx, tempRoot, workspace)
	if err != nil {
		return result, err
	}
	defer func() { _ = store.Close() }()

	firstRun, firstErr := runOnce(ctx, store, sessionID, workspace, scenario, ctrl, rec)
	result.FirstRunError = errorString(firstErr)
	result.Observations = ctrl.Observations()

	if !ctrl.HasFired() && len(scenario.Hooks) > 0 {
		result.Passed = false
		result.Reason = "expected hook never fired"
		result.VerifyExitCode = -1
		rec.recordProviderCallCount(providerCalls(firstRun))
		return result, nil
	}

	plan, err := analyzeResume(ctx, store, sessionID, workspace)
	if err != nil {
		return result, fmt.Errorf("analyze resume: %w", err)
	}
	if plan.CanResume {
		result.ResumeCanResume = boolPtr(true)
		result.ResumeReason = plan.Reason
	} else {
		result.ResumeCanResume = boolPtr(false)
		result.ResumeReason = plan.Reason
	}
	if plan.Recovery != nil {
		result.ResumeRecoveryKind = plan.Recovery.Kind
		result.ResumeRecoveryCallID = plan.Recovery.Call.ID
	}

	if expected := scenario.Recovery.ExpectedResumeCanResume; expected != nil {
		if *expected != plan.CanResume {
			result.Passed = false
			result.Reason = fmt.Sprintf("expected resume can_resume=%v got %v", *expected, plan.CanResume)
			return result, nil
		}
	}
	if scenario.Recovery.ExpectedResumeReasonContains != "" {
		if !strings.Contains(plan.Reason, scenario.Recovery.ExpectedResumeReasonContains) {
			result.Passed = false
			result.Reason = fmt.Sprintf("resume reason %q does not contain %q", plan.Reason, scenario.Recovery.ExpectedResumeReasonContains)
			return result, nil
		}
	}
	if scenario.Recovery.ExpectedRecoveryKind != "" {
		if plan.Recovery == nil {
			result.Passed = false
			result.Reason = "expected recovery action, got none"
			return result, nil
		}
		if plan.Recovery.Kind != scenario.Recovery.ExpectedRecoveryKind {
			result.Passed = false
			result.Reason = fmt.Sprintf("expected recovery kind %q got %q", scenario.Recovery.ExpectedRecoveryKind, plan.Recovery.Kind)
			return result, nil
		}
	}

	if plan.CanResume && plan.Recovery != nil {
		ctrl2, err := New(Schedule{Seed: seed, Fires: nil})
		if err != nil {
			return result, fmt.Errorf("build resume controller: %w", err)
		}
		rec2 := &scriptedRecorder{}
		_, secondErr := runResume(ctx, store, sessionID, workspace, plan, ctrl2, rec2, scenario)
		result.SecondRunError = errorString(secondErr)
		if secondErr != nil {
			result.Passed = false
			result.Reason = fmt.Sprintf("resume run failed: %v", secondErr)
			return result, nil
		}
	}

	verifyExit, verifyErr := runVerify(ctx, scenario.Verify, workspace)
	result.VerifyExitCode = verifyExit
	result.VerifyError = errorString(verifyErr)

	if verifyExit != scenario.Recovery.ExpectedFinalExitCode {
		result.Passed = false
		result.Reason = fmt.Sprintf("verify exit %d expected %d (%s)", verifyExit, scenario.Recovery.ExpectedFinalExitCode, result.VerifyError)
		return result, nil
	}

	if scenario.Recovery.ForbidDuplicateSideEffects {
		baselineHash, err := goodRunBaseline(ctx, fixtureRoot, scenario)
		if err != nil {
			return result, fmt.Errorf("baseline hash: %w", err)
		}
		actualHash, err := workspaceHash(workspace)
		if err != nil {
			return result, fmt.Errorf("workspace hash: %w", err)
		}
		result.SideEffectHashBaseline = baselineHash
		result.SideEffectHashActual = actualHash
		dup := baselineHash != actualHash
		result.DuplicateSideEffects = boolPtr(dup)
		if dup {
			result.Passed = false
			result.Reason = fmt.Sprintf("workspace hash %s differs from good-run baseline %s", actualHash, baselineHash)
			return result, nil
		}
	}

	if scenario.Recovery.ExpectedNoExtraProviderCalls {
		extra := rec.callCount > 2
		result.ExtraProviderCalls = boolPtr(extra)
		if extra {
			result.Passed = false
			result.Reason = fmt.Sprintf("expected ≤2 Provider calls, got %d", rec.callCount)
			return result, nil
		}
	}

	result.Passed = true
	return result, nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func boolPtr(value bool) *bool { return &value }

func openSessionStore(ctx context.Context, tempRoot, projectRoot string) (*session.SQLiteStore, string, error) {
	dataDir := filepath.Join(tempRoot, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, "", fmt.Errorf("create data dir: %w", err)
	}
	store, err := session.OpenSQLite(ctx, session.OpenOptions{
		DataDir:     dataDir,
		ProjectRoot: projectRoot,
		Now:         time.Now,
	})
	if err != nil {
		return nil, "", fmt.Errorf("open store: %w", err)
	}
	sessionID := "chaos-" + randSuffix()
	return store, sessionID, nil
}

// autoApproveBroker lets chaos runs skip the interactive approval flow
// without depending on a TTY. Production code must never import it.
type autoApproveBroker struct{}

func (autoApproveBroker) Decide(context.Context, policy.ApprovalRequest) (policy.ApprovalResponse, error) {
	return policy.ApprovalResponse{Choice: policy.ApprovalApprove}, nil
}

// runOnce drives a single Agent run with the chaos schedule. The
// command/run identities are created inside driveAgent so the context
// recorder, patch journal and emitter all agree on the same run id.
func runOnce(
	ctx context.Context,
	store *session.SQLiteStore,
	sessionID, workspace string,
	scenario Scenario,
	ctrl *Controller,
	rec *scriptedRecorder,
) (*scriptedProvider, error) {
	return driveAgent(ctx, store, sessionID, workspace, scenario, ctrl, rec, nil)
}

// runResume drives an Agent run that resumes the interrupted session.
func runResume(
	ctx context.Context,
	store *session.SQLiteStore,
	sessionID, workspace string,
	plan session.ResumePlan,
	ctrl *Controller,
	rec *scriptedRecorder,
	scenario Scenario,
) (*scriptedProvider, error) {
	return driveAgent(ctx, store, sessionID, workspace, scenario, ctrl, rec, &plan)
}

func driveAgent(
	ctx context.Context,
	store *session.SQLiteStore,
	sessionID, workspace string,
	scenario Scenario,
	ctrl *Controller,
	rec *scriptedRecorder,
	plan *session.ResumePlan,
) (*scriptedProvider, error) {
	runID := "run-" + randSuffix()
	commandID := "cmd-" + randSuffix()
	stub := newScriptedProvider(scenario.Script, scenario.Fixture, workspace)
	chaosProvider := NewProvider(stub, ctrl)
	guard, err := platform.NewPathGuard(workspace)
	if err != nil {
		return nil, fmt.Errorf("new guard: %w", err)
	}
	engine, err := policy.NewEngine(policy.Options{
		Root: workspace, Mode: policy.ModeInteractive,
		CLI: []policy.Rule{
			{Name: "stub", Tool: "read_file", Decision: policy.DecisionAllow},
			{Name: "stub", Tool: "list_files", Decision: policy.DecisionAllow},
			{Name: "stub", Tool: "search_text", Decision: policy.DecisionAllow},
			{Name: "stub", Tool: "write_todos", Decision: policy.DecisionAllow},
			{Name: "stub", Tool: "edit_file", Decision: policy.DecisionAllow},
			{Name: "stub", Tool: "write_file", Decision: policy.DecisionAllow},
			{Name: "stub", Tool: "read_context_source", Decision: policy.DecisionAllow},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("new policy engine: %w", err)
	}
	approveBroker := autoApproveBroker{}
	chaosBroker := NewBroker(approveBroker, ctrl)
	innerRegistry, err := tools.NewRegistry(tools.DefaultTools()...)
	if err != nil {
		return nil, fmt.Errorf("new registry: %w", err)
	}
	chaosRegistry, err := WrapRegistry(innerRegistry, ctrl)
	if err != nil {
		return nil, fmt.Errorf("wrap registry: %w", err)
	}

	payload, err := session.TaskCommandPayload(scenarioFixturePrompt(scenario))
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	commandKind := session.CommandKindResume
	if plan == nil {
		commandKind = session.CommandKindExec
	}
	maxContextTokens := 4096
	// Context-summary scenarios must trigger compactContext so the chaos wrap
	// can fire HookContextSummary. A small budget makes the summary fire
	// deterministically once the agent accumulates a few turns.
	if scenario.Script == ScriptForceSummaryFinish {
		maxContextTokens = 320
		// Pre-arm the controller so the next summary-shaped Stream call fires
		// regardless of which turn the budget kicks in on.
		if err := ctrl.Arm(HookContextSummary, FireAbort); err != nil {
			return nil, fmt.Errorf("arm context summary: %w", err)
		}
	}
	begin, err := store.BeginCommandRun(ctx, session.CommandRunMetadata{
		SessionID:      sessionID,
		CommandID:      commandID,
		RunID:          runID,
		CommandKind:    commandKind,
		CommandPayload: payload,
		ProjectRoot:    workspace,
		Provider:       "stub",
		Model:          "stub",
		StartedAt:      time.Now().UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("begin command: %w", err)
	}
	_ = begin

	recorder, err := store.NewContextRecorder(ctx, sessionID, runID, commandID)
	if err != nil {
		return nil, fmt.Errorf("new context recorder: %w", err)
	}
	patchJournal, err := store.NewPatchJournalRecorder(ctx, sessionID, runID, commandID, guard.Root())
	if err != nil {
		return nil, fmt.Errorf("new patch recorder: %w", err)
	}
	chaosJournal := NewJournal(patchJournal, ctrl)

	downstream := &events.MemorySink{}
	chaosSink := NewSink(downstream, ctrl)
	emitter, err := events.NewEmitter(runID, chaosSink, time.Now)
	if err != nil {
		return nil, fmt.Errorf("new emitter: %w", err)
	}

	request := agent.RunRequest{
		RunID: runID, Task: scenarioFixturePrompt(scenario), Model: "stub", DisplayModel: "stub",
		MaxTurns: 16,
		Security: "controlled",
	}
	if plan != nil && plan.Recovery != nil {
		request.Recovery = &agent.RecoveryAction{
			SourceRunID: plan.Recovery.SourceRunID,
			Call:        plan.Recovery.Call,
			Kind:        plan.Recovery.Kind,
		}
		request.History = plan.History
		request.ContextSummary = plan.ContextSummary
		request.Todos = plan.Todos
	}

	agentRuntime, err := agent.New(agent.Options{
		Provider:         chaosProvider,
		Registry:         chaosRegistry,
		Guard:            guard,
		Policy:           engine,
		Broker:           chaosBroker,
		Now:              time.Now,
		MaxContextTokens: maxContextTokens,
		ContextRecorder:  recorder,
		MutationJournal:  chaosJournal,
		CostEstimator:    cost.NewEstimator("https://stub.example", "stub"),
	})
	if err != nil {
		return nil, fmt.Errorf("new agent: %w", err)
	}
	rec.recordProviderCallCount(0)
	_, runErr := agentRuntime.Run(ctx, request, emitter)
	rec.recordProviderCallCount(stub.callCount)
	return stub, runErr
}

func analyzeResume(ctx context.Context, store *session.SQLiteStore, sessionID, workspace string) (session.ResumePlan, error) {
	service, err := session.NewService(store)
	if err != nil {
		return session.ResumePlan{}, fmt.Errorf("new service: %w", err)
	}
	return service.AnalyzeResume(ctx, sessionID, workspace)
}

func runVerify(ctx context.Context, verify VerifySpec, workspace string) (int, error) {
	if len(verify.Command) == 0 {
		return 0, errors.New("verify.command is empty")
	}
	timeout, err := parseDuration(verify.Timeout)
	if err != nil {
		return -1, err
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(commandContext, verify.Command[0], verify.Command[1:]...)
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(), "GOWORK=off")
	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		fmt.Fprintf(os.Stderr, "%s\n", strings.TrimSpace(string(output)))
	}
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
		return -1, fmt.Errorf("verify timed out after %s", timeout)
	}
	return -1, err
}

func parseDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 30 * time.Second, nil
	}
	timeout, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid verify timeout %q: %w", value, err)
	}
	if timeout <= 0 {
		return 0, errors.New("verify timeout must be positive")
	}
	return timeout, nil
}

// goodRunBaseline executes the scenario script without any chaos fires so
// the resulting workspace represents the "expected final state" after a
// clean agent run. We compare the chaos workspace to this baseline to
// verify the recovery produced the same edits, not more.
func goodRunBaseline(ctx context.Context, fixtureRoot string, scenario Scenario) (string, error) {
	tempRoot, err := os.MkdirTemp("", "mengdie-baseline-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tempRoot) }()
	source, err := FixturePath(fixtureRoot, scenario.Fixture)
	if err != nil {
		return "", err
	}
	workspace := filepath.Join(tempRoot, "workspace")
	if err := copyTree(source, workspace); err != nil {
		return "", err
	}
	store, sessionID, err := openSessionStore(ctx, tempRoot, workspace)
	if err != nil {
		return "", err
	}
	defer func() { _ = store.Close() }()
	ctrl, err := New(Schedule{Seed: 0, Fires: nil})
	if err != nil {
		return "", err
	}
	rec := &scriptedRecorder{}
	if _, err := runOnce(ctx, store, sessionID, workspace, scenario, ctrl, rec); err != nil {
		return "", fmt.Errorf("baseline run: %w", err)
	}
	exit, err := runVerify(ctx, scenario.Verify, workspace)
	if err != nil {
		return "", err
	}
	if exit != 0 {
		return "", fmt.Errorf("baseline verify exit %d", exit)
	}
	return workspaceHash(workspace)
}

func workspaceHash(workspace string) (string, error) {
	hashes := make(map[string]string)
	err := filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, relErr := filepath.Rel(workspace, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		digest := sha256.Sum256(content)
		hashes[relative] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(hashes))
	for k := range hashes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	combined := bytes.NewBuffer(nil)
	for _, k := range keys {
		combined.WriteString(k)
		combined.WriteByte('\n')
		combined.WriteString(hashes[k])
		combined.WriteByte('\n')
	}
	digest := sha256.Sum256(combined.Bytes())
	return hex.EncodeToString(digest[:]), nil
}

func scenarioFixturePrompt(scenario Scenario) string {
	switch scenario.Fixture {
	case "go/sum-boundary":
		return "修复 SumTo 的边界错误，使其包含上界，并保持负数输入返回 0。"
	case "go/normalize-name":
		return "修复 NormalizeName，使其移除首尾空白并转换为小写。"
	case "go/unique-stable":
		return "实现 Unique，使其去除重复字符串并保持首次出现顺序。"
	case "go/strip-extensions":
		return "修复 BaseName，使 archive.tar.gz 返回 archive，同时正确处理无扩展名文件。"
	case "go/average-precision":
		return "修复 Average 的整数除法精度问题，并保持空切片返回 0。"
	}
	return "noop"
}

func providerCalls(stub *scriptedProvider) int {
	if stub == nil {
		return 0
	}
	return stub.callCount
}

// scriptedRecorder captures provider call counts across the run.
type scriptedRecorder struct {
	mu        sync.Mutex
	callCount int
}

func (r *scriptedRecorder) recordProviderCallCount(count int) {
	r.mu.Lock()
	r.callCount = count
	r.mu.Unlock()
}

// scriptedProvider plays a fixed tool-call sequence so chaos runs do not
// depend on a real model Provider. The script defines the sequence; the
// Provider returns the next canned response on every Stream call.
type scriptedProvider struct {
	id        string
	script    string
	fixture   string
	workspace string
	mu        sync.Mutex
	callCount int
}

func newScriptedProvider(script, fixture, workspace string) *scriptedProvider {
	return &scriptedProvider{id: "stub", script: script, fixture: fixture, workspace: workspace}
}

func (p *scriptedProvider) ID() string { return p.id }

func (p *scriptedProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{
		ToolCalling:      true,
		ParallelTools:    true,
		UsageInStream:    true,
		StrictToolSchema: true,
		MaxContextTokens: 32000,
	}, nil
}

func (p *scriptedProvider) Stream(ctx context.Context, req provider.ChatRequest, sink provider.StreamSink) (*provider.ChatResponse, error) {
	p.mu.Lock()
	index := p.callCount
	p.callCount++
	p.mu.Unlock()
	responses := p.scriptResponses(req)
	if index >= len(responses) {
		return &provider.ChatResponse{
			Message: provider.Message{Role: provider.RoleAssistant, Content: "完成"},
		}, nil
	}
	return &responses[index], nil
}

func (p *scriptedProvider) scriptResponses(_ provider.ChatRequest) []provider.ChatResponse {
	switch p.script {
	case ScriptEditThenFinish:
		toolCallID := "edit-call-1"
		edit := p.editCall(toolCallID)
		finish := finishResponse("修复完成")
		return []provider.ChatResponse{edit, finish}
	case ScriptReadThenFinish:
		toolCallID := "read-call-1"
		read := p.readCall(toolCallID)
		finish := finishResponse("已读取")
		return []provider.ChatResponse{read, finish}
	case ScriptForceSummaryFinish:
		// Three-turn flow: read, edit, finish. The second turn has enough
		// accumulated messages for PlanCompaction to fire, and the chaos
		// wrap intercepts the resulting summary stream.
		read := p.readCall("read-call-1")
		edit := p.editCall("edit-call-1")
		finish := finishResponse("修复完成")
		return []provider.ChatResponse{read, edit, finish}
	}
	return []provider.ChatResponse{finishResponse("无脚本")}
}

func (p *scriptedProvider) editCall(toolCallID string) provider.ChatResponse {
	args, _ := json.Marshal(map[string]any{
		"path":                  fixtureRelativePath(p.fixture, "sum.go"),
		"old_text":              "\tfor value := 0; value < limit; value++ {",
		"new_text":              "\tfor value := 0; value <= limit; value++ {",
		"expected_replacements": 1,
	})
	return provider.ChatResponse{
		Message: provider.Message{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{
				ID: toolCallID, Type: "function", Name: "edit_file", Arguments: args,
			}},
		},
	}
}

func (p *scriptedProvider) readCall(toolCallID string) provider.ChatResponse {
	args, _ := json.Marshal(map[string]any{
		"path": fixtureRelativePath(p.fixture, "sum.go"),
	})
	return provider.ChatResponse{
		Message: provider.Message{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{
				ID: toolCallID, Type: "function", Name: "read_file", Arguments: args,
			}},
		},
	}
}

func finishResponse(text string) provider.ChatResponse {
	return provider.ChatResponse{
		Message: provider.Message{Role: provider.RoleAssistant, Content: text},
	}
}

func fixtureRelativePath(fixture, name string) string {
	switch fixture {
	case "go/sum-boundary":
		return name
	}
	return name
}

// copyTree and copyFile are intentionally duplicated from internal/evaluation
// to keep chaos runner independent of the baseline runner package.
func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return fmt.Errorf("resolve fixture entry: %w", err)
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect fixture entry %q: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture entry %q is a symbolic link", relative)
		}
		if entry.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("create fixture directory %q: %w", relative, err)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("fixture entry %q is not a regular file", relative)
		}
		if err := copyFile(path, target, info.Mode().Perm()); err != nil {
			return fmt.Errorf("copy fixture entry %q: %w", relative, err)
		}
		return nil
	})
}

func copyFile(source, destination string, mode fs.FileMode) (err error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := input.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close source fixture: %w", closeErr))
		}
	}()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

// randSuffix returns a short stable random suffix. Tests use it for run /
// command identifiers; deterministic input is enough because we never
// compare runs by id.
func randSuffix() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	value := time.Now().UnixNano()
	out := make([]byte, 8)
	for i := range out {
		out[i] = charset[value%int64(len(charset))]
		value /= int64(len(charset))
	}
	return string(out)
}
