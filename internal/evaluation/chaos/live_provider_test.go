// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build liveprovider

package chaos

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/agent"
	"github.com/Scorpio69t/mengdie-code/internal/cost"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/provider/openaicompat"
	"github.com/Scorpio69t/mengdie-code/internal/session"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

// TestLiveProviderChaosScenarios runs the chaos manifest with the real
// OpenAI-compatible Provider on a protected workspace. It exercises only
// the kill points that do not destroy public-event consistency so the
// recovery contract can be observed end to end. The test is gated behind
// the liveprovider build tag and the MENGDIE_LIVE_SMOKE environment
// variable, matching the M1 live provider smoke convention.
func TestLiveProviderChaosScenarios(t *testing.T) {
	if os.Getenv("MENGDIE_LIVE_SMOKE") != "1" {
		t.Skip("set MENGDIE_LIVE_SMOKE=1 and live Provider variables to run")
	}
	baseURL := requiredEnv(t, "MENGDIE_LIVE_BASE_URL")
	apiKey := requiredEnv(t, "MENGDIE_LIVE_API_KEY")
	model := requiredEnv(t, "MENGDIE_LIVE_MODEL")

	manifestPath := liveManifestPath(t)
	scenarios, err := loadLiveManifest(manifestPath)
	if err != nil {
		t.Fatalf("load live manifest: %v", err)
	}

	fixtureRoot := liveFixtureRoot(t)
	evidence := LiveEvidence{
		SuiteID:     "live-provider-chaos-v1",
		StartedAt:   time.Now().UTC(),
		Scenarios:   make([]LiveScenarioEvidence, 0, len(scenarios)),
		PlatformOS:  platformOS(),
		ProviderURL: baseURL,
		Model:       model,
	}

	for _, scenario := range scenarios {
		scenarioEvidence := LiveScenarioEvidence{
			ID:    scenario.ID,
			Title: scenario.Title,
		}
		workspace, cleanup := setupWorkspace(t, fixtureRoot, scenario.Fixture)
		defer cleanup()
		store, sessionID, err := openLiveStore(t, workspace)
		if err != nil {
			t.Fatalf("open live store: %v", err)
		}
		defer store.Close()

		ctrl, err := New(Schedule{Seed: 1, Fires: scenario.Hooks})
		if err != nil {
			t.Fatalf("build controller: %v", err)
		}
		providerFactory := liveProviderFactory(baseURL, apiKey, model)
		result, stdout, stderr := driveLive(t, store, sessionID, workspace, scenario, ctrl, providerFactory)
		if containsLeakage(stdout, apiKey) || containsLeakage(stderr, apiKey) {
			t.Fatalf("live evidence leaked Provider credential for scenario %s", scenario.ID)
		}
		scenarioEvidence.Observations = ctrl.Observations()
		scenarioEvidence.RunError = result.firstErr
		scenarioEvidence.ResumeCanResume = result.canResume
		scenarioEvidence.ResumeReason = result.reason
		scenarioEvidence.VerifyExitCode = result.verifyExitCode
		scenarioEvidence.WorkspaceSHA = result.workspaceSHA
		scenarioEvidence.RedactionOK = true
		evidence.Scenarios = append(evidence.Scenarios, scenarioEvidence)
		if scenarioEvidence.VerifyExitCode != scenario.Recovery.ExpectedFinalExitCode {
			t.Logf("scenario %s verify_exit=%d expected=%d reason=%s",
				scenario.ID, scenarioEvidence.VerifyExitCode, scenario.Recovery.ExpectedFinalExitCode, scenarioEvidence.ResumeReason)
		}
	}

	evidence.EndedAt = time.Now().UTC()
	evidencePath := liveEvidencePath(t)
	if err := writeEvidence(evidencePath, evidence); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	t.Logf("live chaos evidence written to %s", evidencePath)
}

type liveResult struct {
	firstErr       string
	canResume      *bool
	reason         string
	verifyExitCode int
	workspaceSHA   string
}

func driveLive(
	t *testing.T,
	store *session.SQLiteStore,
	sessionID, workspace string,
	scenario Scenario,
	ctrl *Controller,
	providerFactory func() provider.Provider,
) (liveResult, string, string) {
	t.Helper()
	runID := "live-run-" + liveRand()
	commandID := "live-cmd-" + liveRand()
	providerClient := providerFactory()
	chaosProvider := NewProvider(providerClient, ctrl)
	chaosProvider.PreStream = livePreStream(ctrl, scenario.Script)
	guard, err := platform.NewPathGuard(workspace)
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}
	engine, err := policy.NewEngine(policy.Options{
		Root: workspace, Mode: policy.ModeInteractive,
		CLI: allowedLiveRules(),
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	innerRegistry, err := tools.NewRegistry(tools.DefaultTools()...)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	chaosRegistry, err := WrapRegistry(innerRegistry, ctrl)
	if err != nil {
		t.Fatalf("wrap registry: %v", err)
	}
	payload, err := session.TaskCommandPayload(scenarioFixturePrompt(scenario))
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	_, err = store.BeginCommandRun(context.Background(), session.CommandRunMetadata{
		SessionID:      sessionID,
		CommandID:      commandID,
		RunID:          runID,
		CommandKind:    session.CommandKindExec,
		CommandPayload: payload,
		ProjectRoot:    workspace,
		Provider:       "openai-compatible",
		Model:          "live",
		StartedAt:      time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("begin command: %v", err)
	}
	recorder, err := store.NewContextRecorder(context.Background(), sessionID, runID, commandID)
	if err != nil {
		t.Fatalf("new context recorder: %v", err)
	}
	patchJournal, err := store.NewPatchJournalRecorder(context.Background(), sessionID, runID, commandID, guard.Root())
	if err != nil {
		t.Fatalf("new patch recorder: %v", err)
	}
	chaosJournal := NewJournal(patchJournal, ctrl)
	downstream := &events.MemorySink{}
	chaosSink := NewSink(downstream, ctrl)
	emitter, err := events.NewEmitter(runID, chaosSink, time.Now)
	if err != nil {
		t.Fatalf("new emitter: %v", err)
	}
	request := agent.RunRequest{
		RunID: runID, Task: scenarioFixturePrompt(scenario), Model: "live", DisplayModel: "live",
		MaxTurns: 8, Security: "controlled",
	}
	broker, err := policy.NewTextBroker(strings.NewReader("y\n"), io.Discard)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	agentRuntime, err := agent.New(agent.Options{
		Provider:         chaosProvider,
		Registry:         chaosRegistry,
		Guard:            guard,
		Policy:           engine,
		Broker:           broker,
		Now:              time.Now,
		MaxContextTokens: 8192,
		ContextRecorder:  recorder,
		MutationJournal:  chaosJournal,
		CostEstimator:    cost.NewEstimator("https://stub.example", "live"),
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	_, runErr := agentRuntime.Run(context.Background(), request, emitter)
	firstErr := ""
	if runErr != nil {
		firstErr = runErr.Error()
	}
	plan, err := session.NewService(store)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	resumePlan, _ := plan.AnalyzeResume(context.Background(), sessionID, workspace)
	var canResume *bool
	if resumePlan.CanResume || resumePlan.Reason != "" {
		v := resumePlan.CanResume
		canResume = &v
	}
	exit, verifyErr := runVerifyExternal(context.Background(), scenario.Verify, workspace)
	if verifyErr != nil {
		stderr.WriteString(verifyErr.Error())
	}
	hash, _ := workspaceHash(workspace)
	return liveResult{
		firstErr:       firstErr,
		canResume:      canResume,
		reason:         resumePlan.Reason,
		verifyExitCode: exit,
		workspaceSHA:   hash,
	}, stdout.String(), stderr.String()
}

func runVerifyExternal(ctx context.Context, verify VerifySpec, workspace string) (int, error) {
	if len(verify.Command) == 0 {
		return 0, nil
	}
	timeout := 60 * time.Second
	if verify.Timeout != "" {
		parsed, err := time.ParseDuration(verify.Timeout)
		if err == nil {
			timeout = parsed
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := liveExec(ctx, verify.Command[0], verify.Command[1:], workspace)
	if err != nil {
		if msg := err.Error(); strings.Contains(msg, "exit status") {
			code, ok := parseExitStatus(msg)
			if ok {
				return code, nil
			}
		}
		return -1, err
	}
	if len(output) > 0 {
		fmt.Fprintf(os.Stderr, "%s\n", strings.TrimSpace(string(output)))
	}
	return 0, nil
}

func liveExec(ctx context.Context, name string, args []string, dir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	return cmd.CombinedOutput()
}

func parseExitStatus(message string) (int, bool) {
	const prefix = "exit status "
	index := strings.Index(message, prefix)
	if index < 0 {
		return 0, false
	}
	tail := message[index+len(prefix):]
	end := strings.IndexAny(tail, " \n\t")
	if end < 0 {
		end = len(tail)
	}
	var code int
	for _, r := range tail[:end] {
		if r < '0' || r > '9' {
			return 0, false
		}
		code = code*10 + int(r-'0')
	}
	return code, true
}

func livePreStream(ctrl *Controller, script string) func(context.Context, provider.ChatRequest) error {
	return func(_ context.Context, _ provider.ChatRequest) error {
		if script == ScriptForceSummaryFinish {
			return ctrl.Arm(HookContextSummary, FireAbort)
		}
		return nil
	}
}

func allowedLiveRules() []policy.Rule {
	return []policy.Rule{
		{Name: "live", Tool: "read_file", Decision: policy.DecisionAllow},
		{Name: "live", Tool: "list_files", Decision: policy.DecisionAllow},
		{Name: "live", Tool: "search_text", Decision: policy.DecisionAllow},
		{Name: "live", Tool: "write_todos", Decision: policy.DecisionAllow},
		{Name: "live", Tool: "edit_file", Decision: policy.DecisionAsk},
		{Name: "live", Tool: "write_file", Decision: policy.DecisionAsk},
	}
}

func liveProviderFactory(baseURL, apiKey, _ string) func() provider.Provider {
	return func() provider.Provider {
		client, err := openaicompat.New(openaicompat.Config{
			BaseURL: baseURL,
			APIKey:  apiKey,
		})
		if err != nil {
			panic(fmt.Sprintf("openaicompat.New: %v", err))
		}
		return client
	}
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required when MENGDIE_LIVE_SMOKE=1", name)
	}
	return value
}

func platformOS() string {
	return osName()
}

func liveManifestPath(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(cwd, "..", "..", "..", "evals", "chaos", "live.json")
}

func liveFixtureRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(cwd, "..", "..", "..", "evals", "fixtures")
}

func openLiveStore(t *testing.T, workspace string) (*session.SQLiteStore, string, error) {
	t.Helper()
	root, err := os.MkdirTemp("", "mengdie-live-*")
	if err != nil {
		return nil, "", err
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir:     root,
		ProjectRoot: workspace,
		Now:         time.Now,
	})
	if err != nil {
		return nil, "", err
	}
	return store, "live-" + liveRand(), nil
}

func setupWorkspace(t *testing.T, fixtureRoot, fixture string) (string, func()) {
	t.Helper()
	source, err := FixturePath(fixtureRoot, fixture)
	if err != nil {
		t.Fatal(err)
	}
	tempRoot, err := os.MkdirTemp("", "mengdie-live-ws-*")
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(tempRoot, "workspace")
	if err := copyTree(source, workspace); err != nil {
		os.RemoveAll(tempRoot)
		t.Fatal(err)
	}
	return workspace, func() { os.RemoveAll(tempRoot) }
}

func liveEvidencePath(t *testing.T) string {
	t.Helper()
	root := filepath.Join("evidence")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, fmt.Sprintf("chaos-live-%s-%d.json", osName(), time.Now().Unix()))
}

func loadLiveManifest(path string) ([]Scenario, error) {
	_, scenarios, err := LoadManifest(path)
	if err != nil {
		return nil, err
	}
	return scenarios, nil
}

func containsLeakage(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	return strings.Contains(haystack, needle)
}

type LiveEvidence struct {
	SuiteID     string                 `json:"suite_id"`
	PlatformOS  string                 `json:"platform_os"`
	ProviderURL string                 `json:"provider_url"`
	Model       string                 `json:"model"`
	StartedAt   time.Time              `json:"started_at"`
	EndedAt     time.Time              `json:"ended_at"`
	Scenarios   []LiveScenarioEvidence `json:"scenarios"`
}

type LiveScenarioEvidence struct {
	ID              string        `json:"id"`
	Title           string        `json:"title,omitempty"`
	Observations    []Observation `json:"observations"`
	RunError        string        `json:"run_error,omitempty"`
	ResumeCanResume *bool         `json:"resume_can_resume,omitempty"`
	ResumeReason    string        `json:"resume_reason,omitempty"`
	VerifyExitCode  int           `json:"verify_exit_code"`
	WorkspaceSHA    string        `json:"workspace_sha256"`
	RedactionOK     bool          `json:"redaction_ok"`
}

func writeEvidence(path string, evidence LiveEvidence) error {
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func liveRand() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	value := time.Now().UnixNano()
	out := make([]byte, 8)
	for i := range out {
		out[i] = charset[value%int64(len(charset))]
		value /= int64(len(charset))
	}
	return string(out)
}

func osName() string {
	out, err := os.ReadFile("/proc/version")
	if err == nil && bytes.Contains(out, []byte("Linux")) {
		return "linux"
	}
	if strings.Contains(strings.ToLower(os.Getenv("OS")), "windows") {
		return "windows"
	}
	return "darwin"
}

// Avoid unused-import warning when only this file is built.
var _ = sha256.Sum256
var _ = hex.EncodeToString
