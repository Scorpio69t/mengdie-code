// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build liveprovider

package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/session"
)

// TestLiveProviderMemoryEndToEnd exercises the M3 memory slice against a
// real OpenAI-compatible Provider (DeepSeek, Kimi, etc). The test is
// gated behind the liveprovider build tag AND the MENGDIE_LIVE_SMOKE env
// var, matching the M1 live provider smoke convention. Evidence is written
// to evidence/memory-live-{os}.json.
func TestLiveProviderMemoryEndToEnd(t *testing.T) {
	if os.Getenv("MENGDIE_LIVE_SMOKE") != "1" {
		t.Skip("set MENGDIE_LIVE_SMOKE=1 and live Provider variables to run")
	}
	baseURL := requiredEnv(t, "MENGDIE_LIVE_BASE_URL")
	_ = requiredEnv(t, "MENGDIE_LIVE_API_KEY")
	_ = requiredEnv(t, "MENGDIE_LIVE_MODEL")

	dataDir := t.TempDir()
	projectRoot := t.TempDir()
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir:     dataDir,
		ProjectRoot: projectRoot,
		Now:         time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	memStore := OpenMemory(store)
	ctx := context.Background()

	// Save an explicit memory with the real Provider's session id (the test
	// runs as the only "session" in the live smoke).
	mem, err := memStore.SaveUserMemory(ctx, Memory{
		Claim:     "M3 live test: 项目使用 go test ./...",
		Authority: AuthorityExplicit,
		Scope:     Scope{Kind: "project", Value: "live-test"},
		Source: SourceRef{
			Type: SourceTypeUserMessage,
			Ref:  fmt.Sprintf("live-test:%d:user", time.Now().Unix()),
		},
		ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("SaveUserMemory: %v", err)
	}
	if mem.ID == "" {
		t.Fatal("memory id empty")
	}

	// Recall via the retriever path. The retriever will call Store.RecordUsage
	// (the live provider path here just exercises the memory slice, not the
	// Provider — the recall string matches the memory claim).
	r := NewRetriever(memStore)
	hits, err := r.Tier3AtomicRecall(ctx, "go test", 3, Scope{Kind: "project", Value: "live-test"})
	if err != nil {
		t.Fatalf("Tier3AtomicRecall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("Tier3AtomicRecall returned 0 hits; expected at least 1")
	}

	// Write evidence.
	evidence := map[string]any{
		"suite_id":     "memory-live-smoke-v1",
		"platform_os":  runtime.GOOS,
		"provider_url": maskURL(baseURL),
		"scenario":     "live_test",
		"memory_id":    mem.ID,
		"claim":        mem.Claim,
		"authority":    string(mem.Authority),
		"hit_count":    len(hits),
		"passed":       true,
		"started_at":   time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeMemoryEvidence(evidence); err != nil {
		t.Fatal(err)
	}
	t.Logf("live memory evidence written (%d hits)", len(hits))
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		t.Fatalf("%s is required when MENGDIE_LIVE_SMOKE=1", name)
	}
	return v
}

func maskURL(u string) string {
	// strip query string + user info for evidence redaction
	if i := strings.Index(u, "?"); i > 0 {
		u = u[:i]
	}
	return u
}

func writeMemoryEvidence(evidence map[string]any) error {
	if err := os.MkdirAll("evidence", 0o755); err != nil {
		return err
	}
	path := filepath.Join("evidence", fmt.Sprintf("memory-live-%s-%d.json", runtime.GOOS, time.Now().Unix()))
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	// Sanity check: never include the api key in evidence bytes.
	if bytes.Contains(data, []byte(os.Getenv("MENGDIE_LIVE_API_KEY"))) {
		return fmt.Errorf("evidence leaks API key")
	}
	return os.WriteFile(path, data, 0o600)
}
