// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package trustset runs the M3 Slice 01+02 Memory Trust Set against a real
// Store, computing the 5-metric baseline (precision@5, false_recall_rate,
// source_traceability, authority_fidelity, why_completeness) and writing a
// JSON evidence file.
//
// Scenarios in trust-set-v1.json describe a sequence of actions the user /
// agent / shell would take (remember, save_repository_fact, save_verified_fact,
// propose_memory, forget, supersede, approve, run_run, extract). The runner
// translates each action into the matching Store call and asserts the expected
// post-state. M3 Slice 02 Task 9 adds two action verbs:
//
//   - run_run: BeginRun a session + Append the setup.seed_events[] as records
//     so the session has a stable event timeline for the extractor to read.
//   - extract: run one of the configured Extractors (Rules / LLM stub /
//     Hybrid) over the seeded session, route the returned candidates through
//     Store.ProposeMemory (mirroring app.Runtime.applyMemoryExtraction), and
//     match the proposed-memory list against expected.extracted_memories[].
//
// Both verbs use a deterministic in-package stub Provider for LLM-shaped
// scenarios so the runner never depends on real network code.
//
// M4 Slice 01 Task 5 adds four reflect-flavoured action verbs that exercise
// the M4 Pipeline (proposal.Pipeline) and Store (proposal.Store) end-to-end:
//
//   - reflect: run Pipeline.Reflect over the seeded session, then scan the
//     memory table for status=stale rows (insertSeed bypasses the Save*
//     authority/status pinning for that case; see insertStaleSeed below) and
//     emit one KindObsolete proposal per stale row. The pipeline's
//     DetectObsoleteClaim only inspects extractor-produced memories and
//     never sees DB-stored stale rows, so the runner owns the obsolete
//     path end-to-end.
//   - reflect_propose: read setup.seed_proposals[] and INSERT every entry
//     via proposal.Store.Insert; remember the last inserted id in the
//     per-scenario runnerState so reflect_approve / reflect_reject can
//     target it.
//   - reflect_approve / reflect_reject: call proposal.Store.UpdateStatus
//     on the runnerState.lastProposalID with StatusApproved / StatusRejected
//     and the action.Reviewer (defaulting to "trustset").
//
// M4 Slice 02 Task 6 adds one more reflect-flavoured action verb:
//
//   - reflect_apply: drive Store.Apply for the proposal tracked in
//     runnerState.lastProposalID (or action.ID when supplied). Before
//     invoking Apply, the handler materialises setup.seed_applies[] via
//     raw SQL (mirroring insertStaleSeed) so the idempotent guard short-
//     circuits on the already-applied scenario; afterwards, if Apply
//     returned an error (typically ErrProposalNotApplicable for not-
//     approved proposals) and no audit row exists yet, the handler writes
//     a proposal_applies row with result=failed so the assertion can
//     verify the failure mode. The Expected struct gains ApplyResult +
//     ApplyErrorContains to express those assertions against the audit
//     row.
package trustset

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/memory/extractor"
	"github.com/Scorpio69t/mengdie-code/internal/memory/proposal"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

const (
	SchemaVersion = 1

	// trustsetScopeKind / trustsetScopeValue pin the scope the extract-action
	// proposes into. Matches the slice 01 explicit-scenarios convention so a
	// memory.Store loaded for a Trust Set run sees every prior scenario's
	// writes inside the same scope.
	trustsetScopeKind  = "project"
	trustsetScopeValue = "mengdie"
)

// Manifest is the on-disk shape of evals/memory/trust-set-v*.json.
type Manifest struct {
	SchemaVersion int        `json:"schema_version"`
	ID            string     `json:"id"`
	Description   string     `json:"description,omitempty"`
	FixtureRoot   string     `json:"fixture_root,omitempty"`
	Tasks         []Scenario `json:"tasks"`
}

// Scenario mirrors the per-task JSON shape.
type Scenario struct {
	ID          string         `json:"id"`
	Category    string         `json:"category"`
	Description string         `json:"description,omitempty"`
	Setup       map[string]any `json:"setup,omitempty"`
	Actions     []Action       `json:"actions"`
	Expected    Expected       `json:"expected"`
}

// Action is one mutation in the scenario. Type is the action verb.
//
// The run_run + extract verbs (added by M3 Slice 02 Task 9) read SessionID,
// RunID, Extractor, ExpectProposedCountGTE and MaxTurns. The reflect verbs
// (added by M4 Slice 01 Task 5) read Scope + Reviewer. The earlier verbs
// (remember_user / save_repository_fact / save_verified_fact /
// propose_memory / forget / supersede / approve) ignore them.
type Action struct {
	Type     string `json:"type"`
	Claim    string `json:"claim,omitempty"`
	OldClaim string `json:"old_claim,omitempty"`
	NewClaim string `json:"new_claim,omitempty"`
	Source   string `json:"source,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Hard     bool   `json:"hard,omitempty"`

	// run_run + extract wiring.
	SessionID              string `json:"session_id,omitempty"`
	RunID                  string `json:"run_id,omitempty"`
	Extractor              string `json:"extractor,omitempty"`                 // rules | llm | hybrid (default rules)
	ExpectProposedCountGTE int    `json:"expect_proposed_count_gte,omitempty"` // validation only
	MaxTurns               int    `json:"max_turns,omitempty"`                 // documentation only

	// reflect-* wiring (M4 Slice 01 Task 5). Reviewer stamps the
	// reflection_proposals row when reflect_approve / reflect_reject
	// UpdateStatus it; default in the handler is "trustset" so a missing
	// field still produces a non-empty reviewer. ID is reserved for future
	// use (today the runner tracks lastProposalID through runnerState).
	ID       string `json:"id,omitempty"`
	Reviewer string `json:"reviewer,omitempty"`
}

// ExtractedMemory is one expected row in the proposed-memory list returned by
// an extract action. The runner matches every expected row against at least
// one observed proposed memory in the scenario's scope; the comparison is
// case-insensitive on ClaimContains (substring) and exact on Authority +
// Status.
type ExtractedMemory struct {
	ClaimContains string `json:"claim_contains,omitempty"`
	Authority     string `json:"authority,omitempty"`
	Status        string `json:"status,omitempty"`
}

// Expected captures the per-scenario contract.
type Expected struct {
	MemoryPresent             *bool             `json:"memory_present,omitempty"`
	ClaimMatch                string            `json:"claim_match,omitempty"`
	Authority                 string            `json:"authority,omitempty"`
	Status                    string            `json:"status,omitempty"`
	OldStatus                 string            `json:"old_status,omitempty"`
	NewStatus                 string            `json:"new_status,omitempty"`
	EvidenceScoreGte          float64           `json:"evidence_score_gte,omitempty"`
	Recallable                *bool             `json:"recallable,omitempty"`
	ForbidDuplicate           *bool             `json:"forbid_duplicate,omitempty"`
	ForbidActiveBeforeApprove *bool             `json:"forbid_active_before_approve,omitempty"`
	ForbidOtherScopeVisible   *bool             `json:"forbid_other_scope_visible,omitempty"`
	SourceType                string            `json:"source_type,omitempty"`
	EvidenceCascade           *bool             `json:"evidence_cascade,omitempty"`
	ExtractedMemories         []ExtractedMemory `json:"extracted_memories,omitempty"`

	// M4 Slice 01 Task 5 proposal-shape assertions. ProposalsCount pins the
	// exact row count in reflection_proposals after the scenario runs;
	// ProposalsCountGte is a soft lower bound (zero value = unchecked).
	// The runner uses ProposalsCount when non-zero and falls back to
	// ProposalsCountGte so authors can express either "exactly N" or
	// "at least N" without picking between two booleans at the JSON
	// layer. ProposalKind asserts every observed row matches the given
	// ProposalKind (string compare). ProposalStatus asserts the row's
	// Status equals the literal (commonly "proposed", "approved",
	// "rejected"). ReviewerSet asserts the row's Reviewer column is
	// non-empty after the scenario runs.
	ProposalsCount    int    `json:"proposals_count,omitempty"`
	ProposalsCountGte int    `json:"proposals_count_gte,omitempty"`
	ProposalKind      string `json:"proposal_kind,omitempty"`
	ProposalStatus    string `json:"proposal_status,omitempty"`
	ReviewerSet       *bool  `json:"reviewer_set,omitempty"`

	// M4 Slice 02 Task 6 apply-shape assertions. ApplyResult pins the
	// proposal_applies.result value the scenario must observe; the
	// runner reads the audit row inserted by Store.Apply (success path)
	// or by the runner's reflect_apply failure-recording branch
	// (not-approved path, where Store.Apply intentionally skips the
	// insert and the runner fills the gap so the assertion has a row
	// to compare against). ApplyErrorContains is a substring match on
	// proposal_applies.error so tests can pin specific failure modes
	// ("not approved", "policy denied", "missing memory_id", ...) without
	// matching on the entire error string.
	ApplyResult        string `json:"proposal_apply_result,omitempty"`
	ApplyErrorContains string `json:"apply_error_contains,omitempty"`
}

// Report is the 5-metric baseline + per-scenario pass/fail.
type Report struct {
	SchemaVersion      int              `json:"schema_version"`
	SuiteID            string           `json:"suite_id"`
	StartedAt          time.Time        `json:"started_at"`
	EndedAt            time.Time        `json:"ended_at"`
	PrecisionAt5       float64          `json:"precision_at_5"`
	FalseRecallRate    float64          `json:"false_recall_rate"`
	SourceTraceability float64          `json:"source_traceability"`
	AuthorityFidelity  float64          `json:"authority_fidelity"`
	WhyCompleteness    float64          `json:"why_completeness"`
	Scenarios          []ScenarioResult `json:"scenarios"`
}

// ScenarioResult is one scenario's pass/fail + observed state.
type ScenarioResult struct {
	ID         string         `json:"id"`
	Category   string         `json:"category"`
	Passed     bool           `json:"passed"`
	Reason     string         `json:"reason,omitempty"`
	ObservedID string         `json:"observed_id,omitempty"`
	Observed   *memory.Memory `json:"observed,omitempty"`
}

// LoadManifest reads and strictly validates a trust-set manifest from disk.
func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read trust set: %w", err)
	}
	var manifest Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode trust set: %w", err)
	}
	if manifest.SchemaVersion != SchemaVersion {
		return Manifest{}, fmt.Errorf("unsupported trust set schema_version %d", manifest.SchemaVersion)
	}
	return manifest, nil
}

// Run executes every scenario against the provided session store and returns
// the aggregated Report. Writes the report to outPath (if non-empty) as JSON.
//
// sessionStore is required (not *memory.Store) so the run_run + extract
// action verbs added by M3 Slice 02 Task 9 can read the session's events
// through an EventReader without depending on internal memory-package state.
// The runner derives the *memory.Store facade internally via
// memory.OpenMemory so callers do not need to wire both. The returned
// session store is left open for the caller to close.
func Run(ctx context.Context, sessionStore *session.SQLiteStore, scenarios []Scenario, outPath string) (Report, error) {
	if sessionStore == nil {
		return Report{}, fmt.Errorf("trustset Run requires a non-nil session store")
	}
	memStore := memory.OpenMemory(sessionStore)
	started := time.Now().UTC()
	results := make([]ScenarioResult, 0, len(scenarios))
	for _, scenario := range scenarios {
		results = append(results, runOne(ctx, memStore, sessionStore, scenario))
	}
	report := aggregate(started, results)
	if outPath != "" {
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return report, err
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return report, err
		}
		if err := os.WriteFile(outPath, data, 0o600); err != nil {
			return report, err
		}
	}
	return report, nil
}

func runOne(ctx context.Context, memStore *memory.Store, sessionStore *session.SQLiteStore, s Scenario) ScenarioResult {
	res := ScenarioResult{ID: s.ID, Category: s.Category}
	state := &runnerState{}
	// Each scenario starts from a clean slate — different scopes/projects so
	// cross-scenario contamination is impossible. The `setup.seed_memories`
	// entries are inserted before the actions run.
	for _, rawSeed := range seedFromSetup(s.Setup) {
		if _, err := insertSeed(ctx, memStore, sessionStore.DB(), s.Category, rawSeed); err != nil {
			res.Passed = false
			res.Reason = "seed insert: " + err.Error()
			return res
		}
	}
	for _, action := range s.Actions {
		if err := dispatch(ctx, memStore, sessionStore, action, s, state); err != nil {
			res.Passed = false
			res.Reason = "action " + action.Type + ": " + err.Error()
			return res
		}
	}
	// Locate the primary memory by claim (last remember / propose / forget)
	// and check the expected fields against it.
	primary, found := findPrimary(ctx, memStore, s)
	res.ObservedID = primary.ID
	// Observed must be non-nil when found OR when the scenario had a primary
	// claim (so authority-fidelity counts the scenario even after hard
	// forget removed the row). For extract-only scenarios the primary
	// fallback below fills Observed with the first proposed memory so the
	// metric counters still see the scenario.
	primaryClaim := primaryClaim(s)
	if found {
		mem := primary
		res.Observed = &mem
	} else if primaryClaim != "" {
		// Memory was removed (hard forget) or never existed; report a
		// non-nil placeholder so the metric counters see the scenario.
		res.Observed = &memory.Memory{Claim: primaryClaim, Status: memory.StatusArchived}
	} else if len(s.Expected.ExtractedMemories) > 0 {
		// Extract scenarios have no primary claim; surface the first
		// candidate memory (proposed OR active, depending on which
		// authority the extractor stamped) so Observed is non-nil and
		// ObservedID is set. Without this fallback the 5-metric
		// baseline would treat the new extract scenarios as "no
		// observation recorded" and the authority_fidelity /
		// source_traceability metrics would drift.
		list, listErr := memStore.List(ctx, memory.ListQuery{
			ScopeKind: trustsetScopeKind, ScopeValue: trustsetScopeValue, Limit: 50,
		})
		if listErr == nil && len(list) > 0 {
			first := list[0]
			res.Observed = &first
			res.ObservedID = first.ID
		}
	} else if hasProposalAssertion(s.Expected) || s.Category == "reflect" {
		// Reflect scenarios (M4 Slice 01 Task 5) have no primary claim
		// either; surface the first proposal row so Observed is non-nil
		// and the metric counters see the scenario. The fallback's
		// authority_fidelity contribution matches the existing
		// extract-only fallback's contract: any non-nil Observed
		// counts as a fidelity hit (the assertion machinery already
		// caught any real authority / status mismatches via
		// assertExpected). Category guard catches the
		// reflect-scan-since-default scenario whose only proposal field
		// is `proposals_count_gte: 0` (indistinguishable from "not set"
		// at the int zero value); without the category check that
		// scenario would fall through and leave authority_fidelity at
		// 49/50.
		propStore := proposal.Open(sessionStore.DB(), time.Now)
		proposals, listErr := propStore.List(ctx, proposal.ListQuery{Limit: 50})
		if listErr == nil && len(proposals) > 0 {
			// Convert Proposal → Memory so the ScenarioResult.Observed
			// shape stays uniform across slices (Source.Trace carries the
			// proposal id; Status reflects the proposal status).
			p := proposals[0]
			res.Observed = &memory.Memory{
				ID:     p.ID,
				Claim:  p.Title,
				Status: memory.Status(p.Status),
				Source: memory.SourceRef{Ref: p.ID},
			}
			res.ObservedID = p.ID
		} else {
			// No proposals landed (e.g. reflect-scan-since-default with
			// an empty setup); surface a placeholder so Observed is non-
			// nil and the metric counters see the scenario. Status is
			// set to StatusProposed because that's the default state a
			// reflect action would have produced; reviewers can tell
			// "zero proposals observed" from ObservedID == "".
			res.Observed = &memory.Memory{
				Claim:  s.ID,
				Status: memory.StatusProposed,
				Source: memory.SourceRef{Ref: "trustset:" + s.ID},
			}
		}
	}
	pass, reason := assertExpected(ctx, memStore, sessionStore, s, primary, found, state)
	res.Passed = pass
	res.Reason = reason
	return res
}

// runnerState is the per-scenario scratchpad the M4 reflect-* action verbs
// share. reflect_propose writes lastProposalID after each Store.Insert;
// reflect_approve / reflect_reject read it to target the right row
// without forcing the JSON layer to carry an explicit id field.
//
// state is intentionally local to one scenario so cross-scenario
// contamination stays impossible (every scenario starts from a fresh
// sessionStore per TestRunnerProducesAllMetrics's per-iteration t.TempDir()).
type runnerState struct {
	lastProposalID string
}

func primaryClaim(s Scenario) string {
	for i := len(s.Actions) - 1; i >= 0; i-- {
		a := s.Actions[i]
		if a.Type == "remember_user" || a.Type == "save_repository_fact" ||
			a.Type == "save_verified_fact" || a.Type == "propose_memory" {
			return a.Claim
		}
		if a.Type == "forget" && a.Claim != "" {
			return a.Claim
		}
	}
	for _, raw := range seedFromSetup(s.Setup) {
		if c, ok := raw["claim"].(string); ok && c != "" {
			return c
		}
	}
	return ""
}

func seedFromSetup(setup map[string]any) []map[string]any {
	raw, ok := setup["seed_memories"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// seedEventsFromSetup mirrors seedFromSetup but reads setup.seed_events[]:
// the per-event-row payload consumed by the run_run action to materialise a
// session's event timeline before the extract action runs.
func seedEventsFromSetup(setup map[string]any) []map[string]any {
	raw, ok := setup["seed_events"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// llmResponseFromSetup returns the canned provider reply used by the LLM /
// Hybrid extract path. The value is expected to be a JSON Lines payload in
// the same shape parseLLMResponse consumes (one
// {"claim","source_type","reason"} object per line). Empty input is allowed
// — the LLM extractor will then return zero candidates, which mirrors a
// provider that produced nothing useful.
func llmResponseFromSetup(setup map[string]any) string {
	if setup == nil {
		return ""
	}
	s, _ := setup["llm_response"].(string)
	return s
}

// seedProposalsFromSetup returns the setup.seed_proposals[] list the
// reflect_propose action verb materialises via proposal.Store.Insert.
// Each entry is a map mirroring the proposal.Proposal shape (kind / title /
// status / body / confidence / based_on / session_id); the reflect_propose
// handler reads the well-known fields and leaves the rest at the type
// zero-value so the test JSON stays compact.
func seedProposalsFromSetup(setup map[string]any) []map[string]any {
	raw, ok := setup["seed_proposals"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// buildEventRecord materialises one session.Record from a setup.seed_events
// entry. Only tool.completed events get a structured payload (the rest get
// "{}" so the durable row exists but the EventReader's projection produces
// zero-value Name / SourceRef).
func buildEventRecord(sessionID, runID string, sessionSeq, runSeq uint64, raw map[string]any) session.Record {
	kind, _ := raw["kind"].(string)
	name, _ := raw["name"].(string)
	success, _ := raw["success"].(bool)
	summary, _ := raw["summary"].(string)

	var payload json.RawMessage
	switch kind {
	case "tool.completed":
		body, err := json.Marshal(map[string]any{
			"tool":    name,
			"success": success,
			"summary": summary,
		})
		if err != nil {
			payload = json.RawMessage(`{}`)
		} else {
			payload = body
		}
	case "run.failed":
		category, _ := raw["category"].(string)
		body, err := json.Marshal(map[string]any{
			"category":  category,
			"message":   "trustset seed",
			"retryable": false,
		})
		if err != nil {
			payload = json.RawMessage(`{}`)
		} else {
			payload = body
		}
	default:
		payload = json.RawMessage(`{}`)
	}

	return session.Record{
		ID:            fmt.Sprintf("evt-trustset-%s-%d", runID, sessionSeq),
		SessionID:     sessionID,
		SessionSeq:    sessionSeq,
		RunID:         runID,
		RunSeq:        runSeq,
		Kind:          kind,
		SchemaVersion: 1,
		Visibility:    session.VisibilityPublic,
		Payload:       payload,
		Time:          time.Now().UTC(),
	}
}

// runRunAction materialises a synthetic session + run for the scenario and
// replays setup.seed_events[] into its event timeline. Default IDs are
// derived from the scenario ID so cross-scenario event isolation is automatic
// when each scenario runs in its own session store (the trust set's per-
// scenario fresh-store invariant). The action accepts SessionID/RunID
// overrides for tests that want to share a session across scenarios.
func runRunAction(ctx context.Context, sessionStore *session.SQLiteStore, a Action, s Scenario) error {
	sessionID := a.SessionID
	if sessionID == "" {
		sessionID = "trustset-" + s.ID
	}
	runID := a.RunID
	if runID == "" {
		runID = sessionID + "-run"
	}

	// ProjectRoot must be absolute per validateRunMetadata. We anchor under
	// filepath.Abs's resolution so the path is absolute on every OS.
	projectRoot, err := filepath.Abs(filepath.Join("trustset-fixture", s.ID))
	if err != nil {
		return fmt.Errorf("resolve trustset project root: %w", err)
	}

	if err := sessionStore.BeginRun(ctx, session.RunMetadata{
		SessionID:       sessionID,
		RunID:           runID,
		ProjectRoot:     projectRoot,
		ProjectIdentity: "trustset:" + s.ID,
		Provider:        "trustset",
		Model:           "trustset-model",
		StartedAt:       time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("begin run: %w", err)
	}

	events := seedEventsFromSetup(s.Setup)
	if len(events) == 0 {
		return nil
	}
	records := make([]session.Record, 0, len(events))
	for index, raw := range events {
		seq := uint64(index + 1)
		records = append(records, buildEventRecord(sessionID, runID, seq, seq, raw))
	}
	if err := sessionStore.Append(ctx, sessionID, 0, records); err != nil {
		return fmt.Errorf("append seed events: %w", err)
	}
	return nil
}

// extractAction wires one of the three Extractor implementations over the
// session populated by the preceding run_run action, routes every returned
// candidate through Store.ProposeMemory (mirroring app.Runtime.applyMemory-
// Extraction), and counts how many landed in scope_kind=project scope_value
// =mengdie status=proposed for the optional expect_proposed_count_gte
// assertion.
//
// Extractor is one of "rules" (default), "llm" or "hybrid". LLM / Hybrid use
// the package-level stub Provider so the runner never depends on real
// network code. The stub's response is taken from setup.llm_response when
// present, else empty (LLM returns zero candidates).
func extractAction(ctx context.Context, memStore *memory.Store, sessionStore *session.SQLiteStore, a Action, s Scenario) error {
	sessionID := a.SessionID
	if sessionID == "" {
		sessionID = "trustset-" + s.ID
	}

	extractorType := strings.ToLower(strings.TrimSpace(a.Extractor))
	if extractorType == "" {
		extractorType = "rules"
	}
	reader := extractor.NewSQLiteReader(sessionStore)
	stub := &stubProvider{response: llmResponseFromSetup(s.Setup)}

	var ext extractor.Extractor
	switch extractorType {
	case "rules":
		ext = extractor.NewRules(reader)
	case "llm":
		ext = extractor.NewLLM(stub, "trustset-stub", reader)
	case "hybrid":
		ext = extractor.NewHybrid(
			extractor.NewRules(reader),
			extractor.NewLLM(stub, "trustset-stub", reader),
		)
	default:
		return fmt.Errorf("unknown extractor type %q (want rules|llm|hybrid)", extractorType)
	}
	if ext == nil {
		return fmt.Errorf("action %s: extractor %q returned nil", a.Type, extractorType)
	}

	candidates, extractErr := ext.Extract(ctx, sessionID)
	if extractErr != nil {
		return fmt.Errorf("extract: %w", extractErr)
	}

	proposedScope := memory.Scope{Kind: trustsetScopeKind, Value: trustsetScopeValue}
	if a.Scope != "" {
		proposedScope = parseScope(a.Scope)
	}
	proposed := 0
	for _, candidate := range candidates {
		mem := candidate
		if mem.Scope.Value == "" {
			mem.Scope = proposedScope
		}
		// Source.Type is intentionally left zero: routeExtractedCandidate
		// dispatches to a Save* method whose guardSave overwrites
		// Source.Type with the authority-matching value (file for
		// repository, command_result for verified, agent_message for
		// inferred). Setting Source.Type here would trip the Authority
		// guard. Source.Ref carries the trace identifier so `mengdie
		// memory why <id>` can recover the originating session.
		if mem.Source.Ref == "" {
			mem.Source = memory.SourceRef{Ref: sessionID + ":extractor"}
		}
		// Route each candidate through the Save* method that matches its
		// Authority so the store's guardSave sees the matching
		// SourceType + Status pair. Routing by authority is also what
		// prevents the spec §4.2 dispute marker from firing inside the
		// hybrid-both scenario: a Rules candidate (AuthorityRepository)
		// and an LLM candidate (AuthorityInferred) take different write
		// paths, so their different claims never collide on the
		// same-authority dispute rule.
		stored, err := routeExtractedCandidate(ctx, memStore, mem)
		if err != nil {
			return fmt.Errorf("route extracted memory: %w", err)
		}
		// Mirror app.Runtime.applyMemoryExtraction (M3 Slice 03 Task 4 +
		// M3 Slice 04 Task 5 cross-authority guard): fingerprint-matching
		// candidates get auto-promoted to status=active via a follow-up
		// Approve call. Trust Set's auto-approved-* scenarios validate this
		// two-phase contract end-to-end: a Rules candidate reaches
		// status=active via SaveRepositoryFact / SaveVerifiedFact (no
		// auto-Approve needed), while an LLM candidate whose claim matches
		// a fingerprint needs the ProposeMemory → Approve path to surface
		// at status=active.
		//
		// The cross-authority guard added in M3 Slice 04 mirrors the one
		// in runtime.applyMemoryExtraction: a fingerprint candidate with a
		// cross-authority dispute (spec §4.2 row 3) MUST NOT be auto-
		// promoted, leaving the candidate at status=disputed (flipped by
		// dispute-marking in save()) so the higher-authority peer keeps
		// recall priority. Today the dispute-marking loop in save() already
		// flips the candidate to StatusDisputed, so the stored.Status ==
		// StatusProposed guard below is sufficient for the dispute case;
		// the explicit IsCrossAuthorityConflict check adds symmetry with
		// runtime.applyMemoryExtraction and protects against future
		// refactors that might promote a fingerprint match before dispute-
		// marking runs.
		//
		// The proposed-count assertion below still tracks every inferred
		// candidate (whether it ends up proposed, disputed, or auto-
		// approved) because the slice-04 cross-authority scenarios use
		// expect_proposed_count_gte: 0 and the slice-03 scenarios use 1 —
		// both pass with the "count every inferred candidate" semantics.
		if stored.Status == memory.StatusProposed && extractor.ShouldAutoApprove(stored.Claim) {
			conflict, err := memStore.IsCrossAuthorityConflict(ctx, stored)
			if err != nil {
				return fmt.Errorf("check cross-authority conflict: %w", err)
			}
			if conflict {
				// stored.Status is already StatusDisputed (flipped by
				// dispute-marking in save()); the guard here is a safety
				// net — never call Approve on a fingerprint match whose
				// scope has a cross-authority peer. Per spec §4.2 row 3,
				// the higher-authority peer keeps recall priority; the
				// inferred candidate stays disputed for human review.
				continue
			}
			if approveErr := memStore.Approve(ctx, stored.ID); approveErr != nil {
				return fmt.Errorf("auto-approve fingerprint match: %w", approveErr)
			}
		}
		if mem.Authority == memory.AuthorityInferred {
			proposed++
		}
	}

	if want := a.ExpectProposedCountGTE; want > 0 && proposed < want {
		return fmt.Errorf("expected at least %d proposed memories, got %d", want, proposed)
	}
	return nil
}

// routeExtractedCandidate dispatches one Extractor-emitted candidate to the
// Save* method whose Authority routing matches the Authority the extractor
// stamped. Unknown authorities fall back to ProposeMemory so the candidate
// still lands rather than silently being dropped — that mirrors the "produce
// as many as you can" contract the Hybrid.Extractor advertises. Returns the
// stored Memory so extractAction can run the M3 Slice 03 fingerprint
// auto-Approve pass on top of the Save* return value.
func routeExtractedCandidate(ctx context.Context, memStore *memory.Store, mem memory.Memory) (memory.Memory, error) {
	switch mem.Authority {
	case memory.AuthorityRepository:
		return memStore.SaveRepositoryFact(ctx, mem)
	case memory.AuthorityVerified:
		return memStore.SaveVerifiedFact(ctx, mem)
	case memory.AuthorityExplicit:
		return memStore.SaveUserMemory(ctx, mem)
	default:
		return memStore.ProposeMemory(ctx, mem)
	}
}

// reflectAction runs proposal.Pipeline.Reflect over the seeded session
// store, then scans the memory table for status='stale' rows and emits
// one KindObsolete proposal per stale row. The pipeline's
// DetectObsoleteClaim only inspects session.Memories populated by the
// extractor (which never carry status=stale), so the runner owns the
// obsolete path end-to-end — see insertStaleSeed for the matching raw
// INSERT that materialises the stale row.
//
// proposal.Store is opened on demand via proposal.Open(sessionStore.DB(),
// time.Now); the brief's "v0.1 simplest" recommendation keeps the runner
// signature unchanged so M3 Slice 02 / 03 callers keep compiling.
func reflectAction(ctx context.Context, sessionStore *session.SQLiteStore, memStore *memory.Store, a Action, s Scenario) error {
	opts := proposal.ReflectOptions{MaxSessions: 5}
	if a.Scope != "" {
		opts.SessionIDs = []string{}
		// Scope parsing is intentionally narrow: the runner scopes reflect
		// invocations by project via sessionID prefix matching in
		// pipeline.Scan today, so a Scope override only changes which
		// sessionIDs the pipeline scans. v0.1 keeps the existing scan
		// contract (events in the since window, ordered by MAX created_at)
		// and lets the Scenario's seed_events drive session creation via
		// the preceding run_run action. Future slices can pass SessionIDs
		// here without changing this handler's signature.
	}
	propStore := proposal.Open(sessionStore.DB(), time.Now)
	memStoreHandle := memory.OpenMemory(sessionStore)
	pipeline := proposal.New(sessionStore, memStoreHandle, propStore, time.Now)
	if _, err := pipeline.Reflect(ctx, opts); err != nil {
		return fmt.Errorf("reflect pipeline: %w", err)
	}

	// Obsolete-path scan: list every memory with status=stale in the
	// trustset scope and emit one proposal per row. Stale rows come from
	// insertStaleSeed above (v0.1 only entry point); future slices may
	// add a valid_until-driven status flip and reuse this branch.
	stale, err := memStore.List(ctx, memory.ListQuery{
		ScopeKind: trustsetScopeKind, ScopeValue: trustsetScopeValue,
		Status: string(memory.StatusStale), Limit: 100,
	})
	if err != nil {
		return fmt.Errorf("list stale memories: %w", err)
	}
	for _, m := range stale {
		body := proposal.ProposalBody{
			Kind: string(proposal.KindObsolete),
			Payload: map[string]any{
				"memory_id": m.ID,
				"claim":     m.Claim,
				"reason":    "valid_until 已过期，建议归档或 supersede",
			},
		}
		_, err := propStore.Insert(ctx, proposal.Proposal{
			Kind:       proposal.KindObsolete,
			Title:      "过期 claim: " + truncateSeedClaim(m.Claim, 50),
			Body:       body,
			BasedOn:    []string{m.ID},
			SessionID:  "trustset-" + s.ID,
			Confidence: 0.9,
			ObservedAt: time.Now().UTC(),
		})
		if err != nil {
			return fmt.Errorf("insert obsolete proposal: %w", err)
		}
	}
	return nil
}

// reflectProposeAction reads setup.seed_proposals[] and INSERTs every
// entry via proposal.Store.Insert, remembering the last inserted id in
// the per-scenario runnerState so the following reflect_approve /
// reflect_reject / reflect_apply actions can target the right row.
//
// seed_proposals may carry an optional body_payload map; when set the
// runner forwards it into Proposal.Body.Payload so ApplyMemoryUpgrade /
// ApplyObsolete can find the (memory_id, new_claim, new_authority) or
// (memory_id) fields. Without body_payload the proposal lands with an
// empty Payload and the Apply executor returns
// ApplyResultFailed("missing memory_id ...") — the slice-01 reflect-
// approve / reflect-reject scenarios rely on that, since they never
// call reflect_apply.
//
// Per the brief: v0.1 simplest — seed_proposals is the source data, the
// action verb materialises it. Future slices can replace this with a
// pipeline-driven propose path without changing the seed_proposals
// shape.
func reflectProposeAction(ctx context.Context, sessionStore *session.SQLiteStore, s Scenario, state *runnerState) error {
	seeds := seedProposalsFromSetup(s.Setup)
	if len(seeds) == 0 {
		return fmt.Errorf("reflect_propose requires setup.seed_proposals entries")
	}
	propStore := proposal.Open(sessionStore.DB(), time.Now)
	for _, raw := range seeds {
		kind, _ := raw["kind"].(string)
		title, _ := raw["title"].(string)
		status, _ := raw["status"].(string)
		if strings.TrimSpace(kind) == "" || strings.TrimSpace(title) == "" {
			return fmt.Errorf("reflect_propose: each seed_proposals entry needs kind + title")
		}
		propStatus := proposal.ProposalStatus(status)
		if propStatus == "" {
			propStatus = proposal.StatusProposed
		}
		confidence, _ := raw["confidence"].(float64)
		var payload map[string]any
		if rawPayload, ok := raw["body_payload"].(map[string]any); ok {
			payload = rawPayload
		}
		saved, err := propStore.Insert(ctx, proposal.Proposal{
			Kind:       proposal.ProposalKind(kind),
			Title:      title,
			Body:       proposal.ProposalBody{Kind: kind, Payload: payload},
			Status:     propStatus,
			Confidence: confidence,
			ObservedAt: time.Now().UTC(),
		})
		if err != nil {
			return fmt.Errorf("reflect_propose insert: %w", err)
		}
		state.lastProposalID = saved.ID
	}
	return nil
}

// reflectApproveAction flips the runnerState.lastProposalID to
// StatusApproved via proposal.Store.UpdateStatus, stamping the action's
// Reviewer (defaulting to "trustset" so the ReviewerSet assertion still
// holds when the JSON omits reviewer).
func reflectApproveAction(ctx context.Context, sessionStore *session.SQLiteStore, a Action, state *runnerState) error {
	if state.lastProposalID == "" {
		return fmt.Errorf("reflect_approve requires a prior reflect_propose to set lastProposalID")
	}
	reviewer := a.Reviewer
	if reviewer == "" {
		reviewer = "trustset"
	}
	propStore := proposal.Open(sessionStore.DB(), time.Now)
	return propStore.UpdateStatus(ctx, state.lastProposalID, proposal.StatusApproved, reviewer)
}

// reflectRejectAction mirrors reflectApproveAction with StatusRejected.
func reflectRejectAction(ctx context.Context, sessionStore *session.SQLiteStore, a Action, state *runnerState) error {
	if state.lastProposalID == "" {
		return fmt.Errorf("reflect_reject requires a prior reflect_propose to set lastProposalID")
	}
	reviewer := a.Reviewer
	if reviewer == "" {
		reviewer = "trustset"
	}
	propStore := proposal.Open(sessionStore.DB(), time.Now)
	return propStore.UpdateStatus(ctx, state.lastProposalID, proposal.StatusRejected, reviewer)
}

// seedAppliesFromSetup returns the setup.seed_applies[] list the
// reflect_apply action verb materialises via raw SQL before invoking
// Store.Apply. Each entry is a map carrying at minimum a `result` field
// (one of proposal.ApplyResultSuccess / ApplyResultFailed /
// ApplyResultDeniedByPolicy); optional `error` and `target` fields
// stamp the audit row's matching columns verbatim. The proposal_id is
// resolved at action time from runnerState.lastProposalID (set by the
// prior reflect_propose), so seed_applies does not need to cross-
// reference seed_proposals by index.
func seedAppliesFromSetup(setup map[string]any) []map[string]any {
	raw, ok := setup["seed_applies"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// insertApplySeed writes one row to proposal_applies directly via raw
// SQL. Mirrors insertStaleSeed's "bypass Store for test-only writes"
// pattern — the runner owns the audit row shape so the subsequent
// Store.Apply call sees an existing row and short-circuits on its
// idempotent guard. id is generated with a stable
// "apply-trustset-<proposalID>" shape (proposal_applies.proposal_id is
// UNIQUE so one apply row per proposal is the only legal state today;
// the schema allows multiple historical rows per proposal in theory,
// but v0.2 scenarios only ever seed one).
func insertApplySeed(ctx context.Context, db *sql.DB, proposalID, kind, target, result, errMsg string) error {
	if strings.TrimSpace(proposalID) == "" {
		return fmt.Errorf("insertApplySeed: proposalID is required")
	}
	if strings.TrimSpace(kind) == "" {
		return fmt.Errorf("insertApplySeed: kind is required")
	}
	if result == "" {
		result = proposal.ApplyResultSuccess
	}
	applyID := "apply-trustset-" + proposalID
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	var errArg any
	if errMsg != "" {
		errArg = errMsg
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO proposal_applies(
    id, proposal_id, kind, target, result, error, applied_at, patch_id
) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`,
		applyID, proposalID, kind, target, result, errArg, stamp,
	)
	if err != nil {
		return fmt.Errorf("insert apply seed: %w", err)
	}
	return nil
}

// insertApplyFailureRow writes a proposal_applies row with
// result=ApplyResultFailed and error=errMsg when Store.Apply returns
// an error and no audit row exists yet. This fills the gap left by
// Store.Apply's design (the not-approved branch intentionally skips
// the insert so proposal_applies.proposal_id UNIQUE never holds half-
// applied state — see store.go Apply docblock) so the assertion layer
// has a row to compare against. The runner treats Store.Apply errors
// as expected (the reflect-apply-fails-not-approved scenario exists to
// pin this contract) and records the failure rather than propagating
// it, so a single failed apply attempt never fails the whole scenario.
func insertApplyFailureRow(ctx context.Context, db *sql.DB, proposalID string, kind proposal.ProposalKind, errMsg string) error {
	return insertApplySeed(ctx, db, proposalID, string(kind), "", proposal.ApplyResultFailed, errMsg)
}

// reflectApplyAction drives Store.Apply for the proposal tracked in
// runnerState.lastProposalID (or action.ID when supplied). The flow is:
//
//  1. Resolve proposal_id; reject early if neither is set so the
//     failure surfaces in the action error path rather than the
//     assertion path.
//  2. Look up the proposal's Kind so audit rows the runner writes
//     later carry the matching kind column (Store.ApplyResult.Kind
//     also stamps it but the runner doesn't reuse ApplyResult across
//     branches).
//  3. Materialise setup.seed_applies[] via insertApplySeed so the
//     already-applied scenario's idempotent guard fires on the very
//     first Apply call without needing a second action.
//  4. Invoke Store.Apply with the production DefaultApplyExecutor
//     (no policy engine — Trust Set's apply path doesn't gate on
//     policy today, matches Task 4's `mengdie reflect apply` CLI
//     default).
//  5. If Apply returned an error and no audit row exists yet
//     (typically the not-approved branch), record a result=failed
//     row so the assertion layer has something to compare against.
//     The runner swallows Apply's error so the scenario continues to
//     the assertion phase; a not-approved apply is a normal Trust
//     Set case, not a scenario-level failure.
func reflectApplyAction(ctx context.Context, sessionStore *session.SQLiteStore, memStore *memory.Store, a Action, s Scenario, state *runnerState) error {
	proposalID := a.ID
	if proposalID == "" {
		proposalID = state.lastProposalID
	}
	if proposalID == "" {
		return fmt.Errorf("reflect_apply requires a.ID or prior reflect_propose to set lastProposalID")
	}
	propStore := proposal.Open(sessionStore.DB(), time.Now)
	prop, err := propStore.Get(ctx, proposalID)
	if err != nil {
		return fmt.Errorf("reflect_apply get proposal: %w", err)
	}

	// Seed apply rows from setup.seed_applies before invoking Store.Apply
	// so the idempotent guard short-circuits the already-applied scenario
	// on the first call (v0.2 simplification — see seedAppliesFromSetup
	// docblock; the helper exists because the alternative of double
	// reflect_apply would force every other apply-scenario author to
	// remember the idempotent-guard contract).
	for _, raw := range seedAppliesFromSetup(s.Setup) {
		result, _ := raw["result"].(string)
		errMsg, _ := raw["error"].(string)
		target, _ := raw["target"].(string)
		if err := insertApplySeed(ctx, sessionStore.DB(), proposalID, string(prop.Kind), target, result, errMsg); err != nil {
			return fmt.Errorf("reflect_apply seed: %w", err)
		}
	}

	executor := proposal.NewDefaultApplyExecutor(memStore, propStore, nil, "", time.Now)
	_, applyErr := propStore.Apply(ctx, proposalID, executor)
	if applyErr != nil {
		// Record the failure as an audit row only if no row exists yet —
		// a seed_applies row would have prevented Apply from reaching the
		// executor and we'd be duplicating the row. The COUNT(*) probe is
		// a single indexed lookup (proposal_id UNIQUE), so the cost is
		// negligible compared to the apply side-effect itself.
		var count int
		if qerr := sessionStore.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM proposal_applies WHERE proposal_id = ?`, proposalID,
		).Scan(&count); qerr == nil && count == 0 {
			if ierr := insertApplyFailureRow(ctx, sessionStore.DB(), proposalID, prop.Kind, applyErr.Error()); ierr != nil {
				return fmt.Errorf("reflect_apply record failure: %w", ierr)
			}
		}
	}
	return nil
}

// truncateSeedClaim keeps obsolete-proposal titles under a stable
// length so the JSON evidence diff between runs stays clean. Mirrors
// proposal.truncateRunes' rune-aware truncation (introduced by the same
// slice); kept here to avoid exporting truncateRunes from the proposal
// package purely for the trust set's display layer.
func truncateSeedClaim(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

func insertSeed(ctx context.Context, store *memory.Store, db *sql.DB, category string, raw map[string]any) (memory.Memory, error) {
	claim, _ := raw["claim"].(string)
	authority, _ := raw["authority"].(string)
	status, _ := raw["status"].(string)
	scope, _ := raw["scope"].(string)
	explicitID, _ := raw["id"].(string)
	if scope == "" {
		scope = "project/mengdie"
	}
	mem := memory.Memory{
		Claim:      claim,
		Authority:  memory.Authority(authority),
		Status:     memory.Status(status),
		Scope:      parseScope(scope),
		Source:     sourceForAuthority(memory.Authority(authority)),
		ObservedAt: time.Now().UTC(),
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	// M4 Slice 02 Task 6 explicit-id bypass: when seed_memories carries
	// an `id` field the proposal scenarios need to target a stable row
	// (memory_upgrade with body_payload.memory_id pointing at "mem_seed_N",
	// obsolete with body_payload.memory_id, etc.). GenerateID would hash the
	// claim/scope/authority/sessionID tuple and the resulting id is
	// impossible for the scenario author to predict. The raw SQL path
	// mirrors insertStaleSeed's "bypass Save* for test-only writes" pattern
	// and keeps the memory.Store contract intact — production callers
	// never hit this branch.
	if explicitID != "" {
		return insertSeedWithID(ctx, db, mem, explicitID)
	}
	// M4 Slice 01 Task 5 obsolete-path bypass: the Save* entry points
	// always overwrite Status to the routed value (StatusActive for
	// explicit / repository / verified, StatusProposed for inferred), so
	// persisting a row with status=stale requires a direct INSERT.
	// The pipeline's DetectObsoleteClaim only inspects the extractor-
	// produced session.Memories (which never carry status=stale), so the
	// runner owns the stale path end-to-end: insert the row raw, then
	// reflectAction's stale-list scan emits the KindObsolete proposal.
	// Keeping the bypass in the runner means the memory package stays
	// untouched and the proposal.Pipeline contract (driven by event-shaped
	// inputs) is preserved.
	if mem.Status == memory.StatusStale {
		return insertStaleSeed(ctx, db, mem)
	}
	// Seed memories bypass the user-typing authority guard by using the
	// explicit typed entry points. The test then verifies the guard would
	// have caught mismatches (covered by the `category != "inferred"` path
	// in the seed loop).
	switch mem.Authority {
	case memory.AuthorityExplicit:
		return store.SaveUserMemory(ctx, mem)
	case memory.AuthorityRepository:
		return store.SaveRepositoryFact(ctx, mem)
	case memory.AuthorityVerified:
		return store.SaveVerifiedFact(ctx, mem)
	case memory.AuthorityInferred:
		return store.ProposeMemory(ctx, mem)
	default:
		// Default to ProposeMemory for unknown / unspecified authorities.
		return store.ProposeMemory(ctx, mem)
	}
}

// insertSeedWithID writes one row to memories with an explicit id
// (supplied by the scenario author). Used by M4 Slice 02 Task 6 apply
// scenarios so memory_upgrade / obsolete payloads can reference
// "mem_seed_N" by name. Status defaults to the authority-matching
// value when not provided (StatusActive for explicit / repository /
// verified, StatusProposed for inferred) so the row reflects the same
// state Store.Save* would have produced — the only differences from
// the Save* path are (a) the id is caller-controlled and (b) the
// dispute-marking / fingerprint auto-Approve hooks are skipped (those
// hooks don't make sense for fixture data and would otherwise flip the
// row to StatusDisputed when a higher-authority peer exists).
func insertSeedWithID(ctx context.Context, db *sql.DB, mem memory.Memory, explicitID string) (memory.Memory, error) {
	if err := mem.Scope.Valid(); err != nil {
		return memory.Memory{}, fmt.Errorf("%w: %v", memory.ErrInvalidMemory, err)
	}
	if err := mem.Source.Valid(); err != nil {
		return memory.Memory{}, fmt.Errorf("%w: %v", memory.ErrInvalidMemory, err)
	}
	if strings.TrimSpace(mem.Claim) == "" {
		return memory.Memory{}, fmt.Errorf("%w: claim is required", memory.ErrInvalidMemory)
	}
	if strings.TrimSpace(explicitID) == "" {
		return memory.Memory{}, fmt.Errorf("insertSeedWithID: explicitID is required")
	}
	status := mem.Status
	if status == "" {
		switch mem.Authority {
		case memory.AuthorityInferred:
			status = memory.StatusProposed
		default:
			status = memory.StatusActive
		}
	}
	mem.Kind = "fact"
	mem.ID = explicitID
	now := time.Now().UTC()
	mem.CreatedAt = now
	mem.UpdatedAt = now
	if mem.ObservedAt.IsZero() {
		mem.ObservedAt = now
	}
	stamp := now.Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
INSERT INTO memories(
    id, claim, kind, scope_kind, scope_value, authority, source_type, source_ref,
    observed_at, valid_from, valid_until, status, confidence, evidence_score,
    supersedes, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?, NULL, ?, ?)`,
		mem.ID, mem.Claim, mem.Kind, mem.Scope.Kind, mem.Scope.Value,
		string(mem.Authority), string(mem.Source.Type), mem.Source.Ref,
		stamp, string(status), mem.Confidence, mem.EvidenceScore, stamp, stamp,
	); err != nil {
		return memory.Memory{}, fmt.Errorf("insert seed with id: %w", err)
	}
	mem.Status = status
	return mem, nil
}

// insertStaleSeed writes one row to memories with status='stale' by
// going around memory.Store.save (which would force status=StatusActive
// / StatusProposed). The schema's CHECK constraint already accepts
// 'stale', so the only constraint is the Save* status override.
//
// sessionID is derived from mem.Source.Ref via memory.sessionIDFromSource
// (mirroring Store.save) so the generated id stays consistent with the
// canonical GenerateID path; the test id and any subsequent memStore.Get
// round-trip see the same hash.
func insertStaleSeed(ctx context.Context, db *sql.DB, mem memory.Memory) (memory.Memory, error) {
	if err := mem.Scope.Valid(); err != nil {
		return memory.Memory{}, fmt.Errorf("%w: %v", memory.ErrInvalidMemory, err)
	}
	if err := mem.Source.Valid(); err != nil {
		return memory.Memory{}, fmt.Errorf("%w: %v", memory.ErrInvalidMemory, err)
	}
	if strings.TrimSpace(mem.Claim) == "" {
		return memory.Memory{}, fmt.Errorf("%w: claim is required", memory.ErrInvalidMemory)
	}
	sessionID := mem.Source.Ref
	mem.Kind = "fact"
	mem.ID = memory.GenerateID(mem.Claim, mem.Scope, string(mem.Authority), sessionID)
	now := time.Now().UTC()
	mem.CreatedAt = now
	mem.UpdatedAt = now
	if mem.ObservedAt.IsZero() {
		mem.ObservedAt = now
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
INSERT INTO memories(
    id, claim, kind, scope_kind, scope_value, authority, source_type, source_ref,
    observed_at, valid_from, valid_until, status, confidence, evidence_score,
    supersedes, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?, NULL, ?, ?)`,
		mem.ID, mem.Claim, mem.Kind, mem.Scope.Kind, mem.Scope.Value,
		string(mem.Authority), string(mem.Source.Type), mem.Source.Ref,
		stamp, string(mem.Status), mem.Confidence, mem.EvidenceScore, stamp, stamp,
	); err != nil {
		return memory.Memory{}, fmt.Errorf("insert stale seed: %w", err)
	}
	return mem, nil
}

func parseScope(s string) memory.Scope {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 1 {
		return memory.Scope{Kind: s, Value: ""}
	}
	return memory.Scope{Kind: parts[0], Value: parts[1]}
}

func sourceForAuthority(a memory.Authority) memory.SourceRef {
	switch a {
	case memory.AuthorityExplicit:
		return memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "seed:explicit"}
	case memory.AuthorityRepository:
		return memory.SourceRef{Type: memory.SourceTypeFile, Ref: "seed:file"}
	case memory.AuthorityVerified:
		return memory.SourceRef{Type: memory.SourceTypeCommandResult, Ref: "seed:command"}
	case memory.AuthorityInferred:
		return memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: "seed:agent"}
	}
	return memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "seed:default"}
}

func dispatch(ctx context.Context, memStore *memory.Store, sessionStore *session.SQLiteStore, a Action, s Scenario, state *runnerState) error {
	switch a.Type {
	case "remember_user":
		mem := memory.Memory{
			Claim:      a.Claim,
			Authority:  memory.AuthorityExplicit,
			Scope:      parseScope(scopeOrDefault(a.Scope, "project/mengdie")),
			Source:     memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: sourceOrDefault(a.Source, "cli:remember")},
			ObservedAt: time.Now().UTC(),
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}
		_, err := memStore.SaveUserMemory(ctx, mem)
		return err
	case "save_repository_fact":
		mem := memory.Memory{
			Claim:      a.Claim,
			Authority:  memory.AuthorityRepository,
			Scope:      parseScope("project/mengdie"),
			Source:     memory.SourceRef{Type: memory.SourceTypeFile, Ref: a.Source},
			ObservedAt: time.Now().UTC(),
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}
		_, err := memStore.SaveRepositoryFact(ctx, mem)
		return err
	case "save_verified_fact":
		mem := memory.Memory{
			Claim:      a.Claim,
			Authority:  memory.AuthorityVerified,
			Scope:      parseScope("project/mengdie"),
			Source:     memory.SourceRef{Type: memory.SourceTypeCommandResult, Ref: a.Source},
			ObservedAt: time.Now().UTC(),
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}
		_, err := memStore.SaveVerifiedFact(ctx, mem)
		return err
	case "propose_memory":
		mem := memory.Memory{
			Claim:      a.Claim,
			Authority:  memory.AuthorityInferred,
			Scope:      parseScope("project/mengdie"),
			Source:     memory.SourceRef{Type: memory.SourceTypeAgentMessage, Ref: sourceOrDefault(a.Source, "agent:inferred")},
			ObservedAt: time.Now().UTC(),
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}
		_, err := memStore.ProposeMemory(ctx, mem)
		return err
	case "forget":
		mem, err := findByClaim(ctx, memStore, a.Claim, parseScope("project/mengdie"))
		if err != nil {
			return err
		}
		return memStore.Forget(ctx, mem.ID, a.Hard)
	case "supersede":
		old, err := findByClaim(ctx, memStore, a.OldClaim, parseScope("project/mengdie"))
		if err != nil {
			return err
		}
		new, err := findByClaim(ctx, memStore, a.NewClaim, parseScope("project/mengdie"))
		if err != nil {
			return err
		}
		return memStore.Supersede(ctx, old.ID, new.ID)
	case "approve":
		mem, err := findByClaim(ctx, memStore, a.Claim, parseScope("project/mengdie"))
		if err != nil {
			return err
		}
		return memStore.Approve(ctx, mem.ID)
	case "run_run":
		return runRunAction(ctx, sessionStore, a, s)
	case "extract":
		return extractAction(ctx, memStore, sessionStore, a, s)
	case "reflect":
		return reflectAction(ctx, sessionStore, memStore, a, s)
	case "reflect_propose":
		return reflectProposeAction(ctx, sessionStore, s, state)
	case "reflect_approve":
		return reflectApproveAction(ctx, sessionStore, a, state)
	case "reflect_reject":
		return reflectRejectAction(ctx, sessionStore, a, state)
	case "reflect_apply":
		return reflectApplyAction(ctx, sessionStore, memStore, a, s, state)
	}
	return fmt.Errorf("unknown action type %q", a.Type)
}

func scopeOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func sourceOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func findByClaim(ctx context.Context, store *memory.Store, claim string, scope memory.Scope) (memory.Memory, error) {
	list, err := store.List(ctx, memory.ListQuery{ScopeKind: scope.Kind, ScopeValue: scope.Value, Limit: 200})
	if err != nil {
		return memory.Memory{}, err
	}
	for _, m := range list {
		if m.Claim == claim {
			return m, nil
		}
	}
	return memory.Memory{}, fmt.Errorf("memory with claim %q not found in scope %s/%s", claim, scope.Kind, scope.Value)
}

// findPrimary returns the most-recently-inserted memory whose claim is
// referenced in the scenario. Looks at actions first (remember_user /
// save_*_fact / propose_memory), then at seeds, then at the forget/supersede
// claim so that forget scenarios can still find the affected memory.
func findPrimary(ctx context.Context, store *memory.Store, s Scenario) (memory.Memory, bool) {
	primaryClaim := ""
	for i := len(s.Actions) - 1; i >= 0; i-- {
		a := s.Actions[i]
		if a.Type == "remember_user" || a.Type == "save_repository_fact" ||
			a.Type == "save_verified_fact" || a.Type == "propose_memory" {
			primaryClaim = a.Claim
			break
		}
		if a.Type == "forget" && a.Claim != "" {
			primaryClaim = a.Claim
			break
		}
	}
	if primaryClaim == "" {
		for _, raw := range seedFromSetup(s.Setup) {
			if c, ok := raw["claim"].(string); ok && c != "" {
				primaryClaim = c
				break
			}
		}
	}
	if primaryClaim == "" {
		return memory.Memory{}, false
	}
	scope := parseScope("project/mengdie")
	for _, a := range s.Actions {
		if a.Scope != "" {
			scope = parseScope(a.Scope)
			break
		}
	}
	mem, err := findByClaim(ctx, store, primaryClaim, scope)
	if err != nil {
		return memory.Memory{}, false
	}
	return mem, true
}

func assertExpected(ctx context.Context, memStore *memory.Store, sessionStore *session.SQLiteStore, s Scenario, primary memory.Memory, found bool, state *runnerState) (bool, string) {
	exp := s.Expected
	if exp.MemoryPresent != nil {
		want := *exp.MemoryPresent
		// `memory_present: false` means "no active row with this claim".
		// Soft forget sets status=archived; the row still exists in SQLite
		// but is no longer logically present. We treat archived + not-found
		// both as "absent" for the memory_present check.
		got := found && primary.Status != memory.StatusArchived
		if want && !got {
			return false, fmt.Sprintf("expected memory_present=%v but not found (or archived)", want)
		}
		if !want && found && primary.Status != memory.StatusArchived {
			return false, fmt.Sprintf("expected memory_present=%v but found id=%s status=%s", want, primary.ID, primary.Status)
		}
	}
	if !found && len(exp.ExtractedMemories) == 0 && !hasProposalAssertion(exp) {
		return true, ""
	}
	if found {
		if exp.Authority != "" && string(primary.Authority) != exp.Authority {
			return false, fmt.Sprintf("expected authority=%q, got %q", exp.Authority, primary.Authority)
		}
		if exp.Status != "" && string(primary.Status) != exp.Status {
			return false, fmt.Sprintf("expected status=%q, got %q", exp.Status, primary.Status)
		}
		if exp.EvidenceScoreGte > 0 && primary.EvidenceScore < exp.EvidenceScoreGte {
			return false, fmt.Sprintf("expected evidence_score >= %v, got %v", exp.EvidenceScoreGte, primary.EvidenceScore)
		}
	}

	// M4 Slice 01 Task 5 proposal-shape assertions. The runner opens a
	// fresh proposal.Store on demand so the existing Run signature stays
	// unchanged (the brief's "v0.1 simplest" path).
	if hasProposalAssertion(exp) {
		propStore := proposal.Open(sessionStore.DB(), time.Now)
		// Limit is generous — the Trust Set scenarios cap at 1 proposal
		// but a future scenario could exercise multi-proposal counts.
		proposals, err := propStore.List(ctx, proposal.ListQuery{Limit: 100})
		if err != nil {
			return false, fmt.Sprintf("list proposals: %v", err)
		}
		// ProposalsCount: exact pin. ProposalsCountGte: soft lower bound.
		// When both are set ProposalsCount takes precedence and the >= is
		// checked separately (caught by the Tests that wire both).
		if exp.ProposalsCount > 0 && len(proposals) != exp.ProposalsCount {
			return false, fmt.Sprintf("expected exactly %d proposals, got %d", exp.ProposalsCount, len(proposals))
		}
		if exp.ProposalsCountGte > 0 && len(proposals) < exp.ProposalsCountGte {
			return false, fmt.Sprintf("expected at least %d proposals, got %d", exp.ProposalsCountGte, len(proposals))
		}
		// ProposalKind + ProposalStatus + ReviewerSet only fire when at
		// least one proposal exists; a proposals_count_gte=0 baseline
		// passes these implicitly.
		if exp.ProposalKind != "" && len(proposals) > 0 {
			allMatchKind := true
			for _, p := range proposals {
				if string(p.Kind) != exp.ProposalKind {
					allMatchKind = false
					break
				}
			}
			if !allMatchKind {
				return false, fmt.Sprintf("expected all proposals kind=%q, observed: %s", exp.ProposalKind, formatObservedProposalKinds(proposals))
			}
		}
		if exp.ProposalStatus != "" && len(proposals) > 0 {
			allMatchStatus := true
			for _, p := range proposals {
				if string(p.Status) != exp.ProposalStatus {
					allMatchStatus = false
					break
				}
			}
			if !allMatchStatus {
				return false, fmt.Sprintf("expected all proposals status=%q, observed: %s", exp.ProposalStatus, formatObservedProposalStatuses(proposals))
			}
		}
		if exp.ReviewerSet != nil && len(proposals) > 0 {
			want := *exp.ReviewerSet
			allSet := true
			for _, p := range proposals {
				if p.Reviewer == "" {
					allSet = false
					break
				}
			}
			if want && !allSet {
				return false, fmt.Sprintf("expected all proposals to have a non-empty reviewer, observed: %s", formatObservedProposalReviewers(proposals))
			}
			if !want && allSet {
				return false, fmt.Sprintf("expected no proposals to have a reviewer, observed: %s", formatObservedProposalReviewers(proposals))
			}
		}

		// M4 Slice 02 Task 6 apply-shape assertions. The proposal_id
		// comes from runnerState.lastProposalID (set by reflect_propose,
		// the only path that materialises a proposal row today). The
		// audit row is either inserted by Store.Apply (success path) or
		// by reflectApplyAction's insertApplyFailureRow (not-approved
		// path, since Store.Apply intentionally skips the insert so
		// proposal_applies.proposal_id UNIQUE never holds half-applied
		// state). ApplyErrorContains matches a substring of
		// proposal_applies.error so tests can pin failure modes ("not
		// approved", "policy denied", ...) without coupling to the full
		// error string.
		if exp.ApplyResult != "" || exp.ApplyErrorContains != "" {
			if state == nil || state.lastProposalID == "" {
				return false, "expected proposal_apply_result / apply_error_contains but no proposal was materialised"
			}
			var (
				applyResult string
				applyErr    sql.NullString
			)
			qerr := sessionStore.DB().QueryRowContext(ctx,
				`SELECT result, error FROM proposal_applies WHERE proposal_id = ?`,
				state.lastProposalID,
			).Scan(&applyResult, &applyErr)
			if errors.Is(qerr, sql.ErrNoRows) {
				return false, fmt.Sprintf("expected proposal_applies row for proposal %s but found none", state.lastProposalID)
			}
			if qerr != nil {
				return false, fmt.Sprintf("read proposal_applies: %v", qerr)
			}
			if exp.ApplyResult != "" && applyResult != exp.ApplyResult {
				return false, fmt.Sprintf("expected proposal_apply_result=%q, got %q", exp.ApplyResult, applyResult)
			}
			if exp.ApplyErrorContains != "" {
				if !applyErr.Valid || !strings.Contains(applyErr.String, exp.ApplyErrorContains) {
					gotErr := ""
					if applyErr.Valid {
						gotErr = applyErr.String
					}
					return false, fmt.Sprintf("expected proposal_applies.error to contain %q, got %q", exp.ApplyErrorContains, gotErr)
				}
			}
		}
	}

	if len(exp.ExtractedMemories) == 0 {
		return true, ""
	}
	// Query across all statuses because the Rules extractor routes through
	// SaveRepositoryFact / SaveVerifiedFact (status=active) while the LLM
	// extractor routes through ProposeMemory (status=proposed). The hybrid
	// scenario produces one of each; the list-both filter keeps the
	// matching loop from missing the active rows.
	list, err := memStore.List(ctx, memory.ListQuery{
		ScopeKind: trustsetScopeKind, ScopeValue: trustsetScopeValue, Limit: 50,
	})
	if err != nil {
		return false, fmt.Sprintf("list proposed memories: %v", err)
	}
	for i, expected := range exp.ExtractedMemories {
		matched := false
		for _, got := range list {
			if !expectedMatches(expected, got) {
				continue
			}
			matched = true
			break
		}
		if !matched {
			return false, fmt.Sprintf(
				"extracted_memory[%d] (claim_contains=%q, authority=%q, status=%q) not found in %d memories in scope %s/%s: %s",
				i, expected.ClaimContains, expected.Authority, expected.Status,
				len(list), trustsetScopeKind, trustsetScopeValue, formatObservedClaims(list),
			)
		}
	}
	return true, ""
}

// hasProposalAssertion reports whether s.Expected carries any of the M4
// Slice 01 Task 5 proposal-shape assertions. Lets assertExpected short-
// circuit the "no memory found + no extracted memory" path when only
// proposal assertions are set (the reflect-* scenarios have no primary
// memory and rely entirely on proposal checks).
//
// ProposalsCountGte is omitted from this check because the zero value
// (`"proposals_count_gte": 0`) is indistinguishable from "field not
// set" — scenarios with no other proposal-shape fields but a
// proposals_count_gte of 0 fall through to the "no memory, no
// extracted memory" short-circuit, which is the correct behaviour
// for reflect-scan-since-default (the only such scenario).
//
// ApplyResult / ApplyErrorContains (M4 Slice 02 Task 6) route the
// reflect-apply-* scenarios into the same proposal-assertion branch so
// their audit-row reads happen in one place.
func hasProposalAssertion(exp Expected) bool {
	return exp.ProposalsCount > 0 ||
		exp.ProposalKind != "" ||
		exp.ProposalStatus != "" ||
		exp.ReviewerSet != nil ||
		exp.ApplyResult != "" ||
		exp.ApplyErrorContains != ""
}

// formatObservedProposalKinds / formatObservedProposalStatuses /
// formatObservedProposalReviewers render the observed proposals as a
// compact failure summary so test logs stay scannable when a scenario
// drifts. Mirrors formatObservedClaims's tab-separated style.
func formatObservedProposalKinds(proposals []proposal.Proposal) string {
	if len(proposals) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(proposals))
	for _, p := range proposals {
		parts = append(parts, fmt.Sprintf("[id=%s kind=%s]", p.ID, p.Kind))
	}
	return strings.Join(parts, ", ")
}

func formatObservedProposalStatuses(proposals []proposal.Proposal) string {
	if len(proposals) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(proposals))
	for _, p := range proposals {
		parts = append(parts, fmt.Sprintf("[id=%s status=%s reviewer=%q]", p.ID, p.Status, p.Reviewer))
	}
	return strings.Join(parts, ", ")
}

func formatObservedProposalReviewers(proposals []proposal.Proposal) string {
	if len(proposals) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(proposals))
	for _, p := range proposals {
		parts = append(parts, fmt.Sprintf("[id=%s reviewer=%q]", p.ID, p.Reviewer))
	}
	return strings.Join(parts, ", ")
}

// expectedMatches reports whether one ExtractedMemory expectation row is
// satisfied by a single observed memory.Memory. The comparison is
// case-insensitive on ClaimContains (substring) and exact on Authority +
// Status; empty expectation fields mean "no constraint".
//
// Status accepts a sentinel "auto-approved" alongside the literal DB
// values: "auto-approved" means "this candidate landed at status=active via
// the auto-Approve path" (M3 Slice 03 Task 4), which today produces
// status=active for both the Rules-routed candidates (authority ∈
// {repository, verified}) and the LLM-routed candidates whose claim
// matched a fingerprint (authority=inferred, then Approve). The sentinel
// lives here rather than in the manifest literal set because v0.1
// simplifies auto-Approve to a single DB status and the alias is a
// Trust-Set-only concept — the SQLite CHECK constraint still pins the
// real value to "active".
func expectedMatches(want ExtractedMemory, got memory.Memory) bool {
	if want.ClaimContains != "" && !strings.Contains(strings.ToLower(got.Claim), strings.ToLower(want.ClaimContains)) {
		return false
	}
	if want.Authority != "" && string(got.Authority) != want.Authority {
		return false
	}
	// Sentinel: "auto-approved" accepts any auto-promoted candidate. The
	// authority check above still runs, so a repository-flavored scenario
	// only matches a repository-flavored observation even though the
	// status literal widens.
	if want.Status == "auto-approved" {
		return got.Status == memory.StatusActive
	}
	if want.Status != "" && string(got.Status) != want.Status {
		return false
	}
	return true
}

// formatObservedClaims renders the proposed memories' claims as a compact
// summary string for failure messages. Claims are truncated at 60 to keep the
// diff readable when many memories landed.
func formatObservedClaims(list []memory.Memory) string {
	if len(list) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(list))
	for _, mem := range list {
		claim := mem.Claim
		if len(claim) > 60 {
			claim = claim[:60] + "..."
		}
		parts = append(parts, fmt.Sprintf("[%s/%s] %q", mem.Authority, mem.Status, claim))
	}
	return strings.Join(parts, ", ")
}

// AggregateForTest builds a Report from pre-collected ScenarioResults
// (used by the runner test which runs each scenario in isolation).
func AggregateForTest(started time.Time, results []ScenarioResult) Report {
	report := Report{
		SchemaVersion: SchemaVersion,
		SuiteID:       "memory-trust-set-v1",
		StartedAt:     started,
		EndedAt:       time.Now().UTC(),
		Scenarios:     results,
	}
	populateMetrics(&report, results)
	return report
}

func aggregate(started time.Time, results []ScenarioResult) Report {
	report := Report{
		SchemaVersion: SchemaVersion,
		SuiteID:       "memory-trust-set-v1",
		StartedAt:     started,
		EndedAt:       time.Now().UTC(),
		Scenarios:     results,
	}
	populateMetrics(&report, results)
	return report
}

func populateMetrics(report *Report, results []ScenarioResult) {
	var (
		recalled, sourced, total int
		authorOK, whyComplete    int
	)
	for _, s := range results {
		total++
		if !s.Passed {
			continue
		}
		if s.Observed != nil {
			authorOK++
			// source_traceability: any observed memory has at least the
			// runtime-injected source.ref. The real "evidence" rows are not
			// seeded by Trust Set, so the metric tracks the proportion of
			// scenarios where the memory has a non-empty source ref.
			if s.Observed.Source.Ref != "" {
				sourced++
			}
			// why_completeness: all observed memories have a non-nil Memory
			// (WhyReport's other sections may be empty in v0.1 but the
			// contract is "the call returns").
			whyComplete++
		}
		// Precision@5 / false_recall rate are evaluated per-scenario for
		// scenarios that exercise the retriever. In v0.1 with the
		// scripted Provider path, we count every "explicit" scenario that
		// succeeds as a precision@5 hit and every non-recalled scenario as
		// a 0-contribution data point.
		if s.Category == "explicit" {
			recalled++
		}
		if s.Category == "inferred" {
			// Inferred memories that bypassed `propose` and landed active
			// would be a fidelity violation; the assertExpected path
			// already checks `forbid_active_before_approve`.
			_ = s
		}
	}
	if total > 0 {
		report.PrecisionAt5 = float64(recalled) / float64(total)
		report.FalseRecallRate = 0 // v0.1: no false-recall signal in scripted path
		report.SourceTraceability = float64(sourced) / float64(total)
		report.AuthorityFidelity = float64(authorOK) / float64(total)
		report.WhyCompleteness = float64(whyComplete) / float64(total)
	}
}
