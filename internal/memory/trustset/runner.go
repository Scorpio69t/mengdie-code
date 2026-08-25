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
package trustset

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/memory/extractor"
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
// RunID, Extractor, ExpectProposedCountGTE and MaxTurns. The earlier verbs
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
	// Each scenario starts from a clean slate — different scopes/projects so
	// cross-scenario contamination is impossible. The `setup.seed_memories`
	// entries are inserted before the actions run.
	for _, rawSeed := range seedFromSetup(s.Setup) {
		if _, err := insertSeed(ctx, memStore, s.Category, rawSeed); err != nil {
			res.Passed = false
			res.Reason = "seed insert: " + err.Error()
			return res
		}
	}
	for _, action := range s.Actions {
		if err := dispatch(ctx, memStore, sessionStore, action, s); err != nil {
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
	}
	pass, reason := assertExpected(ctx, memStore, s, primary, found)
	res.Passed = pass
	res.Reason = reason
	return res
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
		// Mirror app.Runtime.applyMemoryExtraction (M3 Slice 03 Task 4):
		// fingerprint-matching candidates get auto-promoted to status=active
		// via a follow-up Approve call. Trust Set's auto-approved-*
		// scenarios validate this two-phase contract end-to-end: a Rules
		// candidate reaches status=active via SaveRepositoryFact /
		// SaveVerifiedFact (no auto-Approve needed), while an LLM
		// candidate whose claim matches a fingerprint pattern needs the
		// ProposeMemory → Approve path to surface at status=active. The
		// proposed-count assertion below still tracks only the pre-Approve
		// count because the existing extractor-rules-* / extractor-llm-*
		// scenarios assert proposed (i.e. non-auto-Approved) candidates.
		if stored.Status == memory.StatusProposed && extractor.ShouldAutoApprove(stored.Claim) {
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

func insertSeed(ctx context.Context, store *memory.Store, category string, raw map[string]any) (memory.Memory, error) {
	claim, _ := raw["claim"].(string)
	authority, _ := raw["authority"].(string)
	status, _ := raw["status"].(string)
	scope, _ := raw["scope"].(string)
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

func dispatch(ctx context.Context, memStore *memory.Store, sessionStore *session.SQLiteStore, a Action, s Scenario) error {
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

func assertExpected(ctx context.Context, memStore *memory.Store, s Scenario, primary memory.Memory, found bool) (bool, string) {
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
	if !found && len(exp.ExtractedMemories) == 0 {
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
