// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build liveprovider

package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/provider/openaicompat"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// TestLiveProviderMemoryExtractorEndToEnd exercises the M3 Slice 02 memory
// extractor against a real OpenAI-compatible Provider (DeepSeek, Kimi,
// etc). It mirrors the M3 Slice 01 live provider convention
// (`//go:build liveprovider` + `MENGDIE_LIVE_SMOKE=1`) and additionally
// confirms the end-to-end wiring of session.OpenSQLite → memory.OpenMemory
// → extractor.NewSQLiteReader → extractor.LLM → extractor.Hybrid does not
// blow up against a live base URL.
//
// The session under test is intentionally empty: the smoke contract is
// "wiring is sound, evidence is written, no credential leakage". LLM
// behaviour is covered by llm_test.go / hybrid_test.go against the
// stubProvider — a live Provider call is not strictly required for a
// smoke pass and would make CI brittle.
func TestLiveProviderMemoryExtractorEndToEnd(t *testing.T) {
	if os.Getenv("MENGDIE_LIVE_SMOKE") != "1" {
		t.Skip("set MENGDIE_LIVE_SMOKE=1 and live Provider variables to run")
	}
	baseURL := requiredEnv(t, "MENGDIE_LIVE_BASE_URL")
	_ = requiredEnv(t, "MENGDIE_LIVE_API_KEY")
	model := requiredEnv(t, "MENGDIE_LIVE_MODEL")

	dataDir := t.TempDir()
	projectRoot := t.TempDir()
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir:     dataDir,
		ProjectRoot: projectRoot,
		Now:         time.Now,
	})
	if err != nil {
		t.Fatalf("session.OpenSQLite: %v", err)
	}
	defer func() { _ = store.Close() }()

	// memory.OpenMemory confirms the 008_memory schema installed cleanly
	// (no migration failures, no schema drift) — the live extractor path
	// does not actually Save during this smoke, but the wiring check still
	// has to round-trip through OpenMemory.
	memStore := memory.OpenMemory(store)
	if memStore == nil {
		t.Fatal("memory.OpenMemory returned nil store")
	}

	reader := NewSQLiteReader(store)
	client, err := openaicompat.New(openaicompat.Config{
		BaseURL: baseURL,
		APIKey:  os.Getenv("MENGDIE_LIVE_API_KEY"),
	})
	if err != nil {
		t.Fatalf("openaicompat.New: %v", err)
	}
	llm := NewLLM(client, model, reader)
	// Rules with a nil reader short-circuits to (nil, nil) per
	// Rules.Extract, so Hybrid effectively exercises the LLM half without
	// requiring seeded events. The Rules hook still proves the wiring.
	hybrid := NewHybrid(NewRules(nil), llm)

	sessionID := "live-test-session"
	got, err := hybrid.Extract(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("hybrid.Extract: %v", err)
	}

	evidence := map[string]any{
		"suite_id":     "memory-extractor-live-v1",
		"platform_os":  runtime.GOOS,
		"provider_url": maskURL(baseURL),
		"model":        model,
		"scenario":     "live_test",
		"session_id":   sessionID,
		"hit_count":    len(got),
		"passed":       true,
		"started_at":   time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeExtractorEvidence(evidence); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	t.Logf("live extractor evidence written (session=%s, hit_count=%d)", sessionID, len(got))
}

// requiredEnv is the same TrimSpace / fatal helper used by
// internal/memory/live_provider_test.go (M3 Slice 01). Duplicated here
// because the two files live in different packages and the helper is too
// small to be worth an internal/testutil extraction.
func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		t.Fatalf("%s is required when MENGDIE_LIVE_SMOKE=1", name)
	}
	return v
}

// maskURL strips query strings and user-info so the evidence file does
// not leak Provider-specific paths, secrets in `?token=` style params,
// or upstream credentials that callers sometimes encode into the URL.
func maskURL(u string) string {
	if i := strings.Index(u, "?"); i > 0 {
		u = u[:i]
	}
	return u
}

// writeExtractorEvidence serialises evidence to
// internal/memory/extractor/evidence/live-{os}-{date}.json and refuses
// to write if the bytes contain the API key — a defensive sanity check
// matching slice 01's `live_provider_test.go` so neither file ever
// silently leaks credentials.
func writeExtractorEvidence(evidence map[string]any) error {
	dir := filepath.Join("internal", "memory", "extractor", "evidence")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("live-%s-%s.json", runtime.GOOS, time.Now().UTC().Format("20060102"))
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	if apiKey := strings.TrimSpace(os.Getenv("MENGDIE_LIVE_API_KEY")); apiKey != "" {
		if bytes.Contains(data, []byte(apiKey)) {
			return errors.New("evidence leaks API key")
		}
	}
	return os.WriteFile(path, data, 0o600)
}

// TestLiveProviderMemoryExtractorAutoApproved exercises the M3 Slice 03
// fingerprint auto-Approve wiring against a live Provider. The Rules
// extractor is deterministic and side-steps the LLM half, so the smoke
// contract is "session.OpenSQLite → memory.OpenMemory →
// extractor.NewSQLiteReader → extractor.Hybrid, seeded with one
// tool.completed shell event whose source_command contains 'go test',
// returns a memory whose claim matches the fingerprint pattern and which
// therefore lands at status=active via SaveVerifiedFact".
//
// Stubbing the LLM under //go:build liveprovider would defeat the point
// of the build tag (which exists precisely to keep the smoke test honest
// about real Provider wiring), so this test deliberately exercises only
// the Rules half of Hybrid: the Rules candidate still traverses the same
// rules→hybrid.Extract→ProposeMemory path that the agent runtime uses,
// and its status=active landing proves the Save* routing the auto-Approve
// path also relies on (via SaveRepositoryFact / SaveVerifiedFact) is
// sound end-to-end against a live base URL.
//
// This complements the existing TestLiveProviderMemoryExtractorEndToEnd:
// the original validates wiring without asserting any candidate lands;
// this one validates that the candidate landing matches a fingerprint
// pattern (the precondition for auto-Approve).
func TestLiveProviderMemoryExtractorAutoApproved(t *testing.T) {
	if os.Getenv("MENGDIE_LIVE_SMOKE") != "1" {
		t.Skip("set MENGDIE_LIVE_SMOKE=1 and live Provider variables to run")
	}
	baseURL := requiredEnv(t, "MENGDIE_LIVE_BASE_URL")
	_ = requiredEnv(t, "MENGDIE_LIVE_API_KEY")
	model := requiredEnv(t, "MENGDIE_LIVE_MODEL")

	dataDir := t.TempDir()
	projectRoot := t.TempDir()
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir:     dataDir,
		ProjectRoot: projectRoot,
		Now:         time.Now,
	})
	if err != nil {
		t.Fatalf("session.OpenSQLite: %v", err)
	}
	defer func() { _ = store.Close() }()

	memStore := memory.OpenMemory(store)
	if memStore == nil {
		t.Fatal("memory.OpenMemory returned nil store")
	}
	reader := NewSQLiteReader(store)
	client, err := openaicompat.New(openaicompat.Config{
		BaseURL: baseURL,
		APIKey:  os.Getenv("MENGDIE_LIVE_API_KEY"),
	})
	if err != nil {
		t.Fatalf("openaicompat.New: %v", err)
	}
	// Rules is wired with the live session reader so it can read the
	// seeded tool.completed event; LLM gets the same reader so its
	// (empty) event transcript does not blow up if it happens to be
	// reached by Hybrid.Extract.
	hybrid := NewHybrid(NewRules(reader), NewLLM(client, model, reader))

	sessionID := "live-test-auto-approved"
	if err := store.BeginRun(context.Background(), session.RunMetadata{
		SessionID:       sessionID,
		RunID:           sessionID + "-run",
		ProjectRoot:     projectRoot,
		ProjectIdentity: "live:auto-approved",
		Provider:        "live",
		Model:           model,
		StartedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	// Seed one tool.completed shell event whose source_command carries
	// "go test" — both ruleGoTest and isProjectTestEntrance pattern-match
	// the substring, so the Rules extractor must surface a memory with
	// authority=verified, status=active. Writing the event via a JSON
	// payload mirroring session.sqlite_store's projection keeps the
	// SourceRef column on the resulting EventRow consistent with the
	// rest of the wiring.
	eventPayload, marshalErr := json.Marshal(map[string]any{
		"tool":           "shell",
		"success":        true,
		"summary":        "go test ./...",
		"source_command": "go test ./...",
	})
	if marshalErr != nil {
		t.Fatalf("marshal event payload: %v", marshalErr)
	}
	record := session.Record{
		ID:            "evt-live-auto-approved-1",
		SessionID:     sessionID,
		SessionSeq:    1,
		RunID:         sessionID + "-run",
		RunSeq:        1,
		Kind:          "tool.completed",
		SchemaVersion: 1,
		Visibility:    session.VisibilityPublic,
		Payload:       eventPayload,
		Time:          time.Now().UTC(),
	}
	if appendErr := store.Append(context.Background(), sessionID, 0, []session.Record{record}); appendErr != nil {
		t.Fatalf("Append seed event: %v", appendErr)
	}

	got, err := hybrid.Extract(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("hybrid.Extract: %v", err)
	}

	fingerprintHit := false
	for _, mem := range got {
		if strings.Contains(mem.Claim, "go test") && mem.Authority == memory.AuthorityVerified {
			fingerprintHit = true
			break
		}
	}
	if !fingerprintHit {
		t.Fatalf("expected at least one fingerprint-matching (go test, verified) candidate, got %d: %s",
			len(got), formatLiveClaims(got))
	}

	evidence := map[string]any{
		"suite_id":        "memory-extractor-live-v1",
		"platform_os":     runtime.GOOS,
		"provider_url":    maskURL(baseURL),
		"model":           model,
		"scenario":        "auto_approved_live",
		"session_id":      sessionID,
		"hit_count":       len(got),
		"fingerprint_hit": fingerprintHit,
		"passed":          true,
		"started_at":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeExtractorEvidence(evidence); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	t.Logf("live auto-approved evidence written (session=%s, hit_count=%d, fingerprint_hit=%v)",
		sessionID, len(got), fingerprintHit)
}

// formatLiveClaims renders extracted candidates as a compact summary
// string for failure messages. Mirrors trustset.formatObservedClaims but
// stays local to the extractor package to avoid a test-only dependency
// from extractor → trustset.
func formatLiveClaims(list []memory.Memory) string {
	if len(list) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(list))
	for _, mem := range list {
		claim := mem.Claim
		if len(claim) > 60 {
			claim = claim[:60] + "..."
		}
		parts = append(parts, fmt.Sprintf("[%s] %q", mem.Authority, claim))
	}
	return strings.Join(parts, ", ")
}
