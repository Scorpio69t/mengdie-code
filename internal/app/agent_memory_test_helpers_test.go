// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/platform"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/session"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

// setupMemoryStoreWithSeeds opens an isolated session SQLite store (which
// applies the 008_memory migration via OpenSQLite), wraps it in memory.Store,
// and seeds two project-scope memories: one explicit and one repository. The
// seeds exercise both the Authority value used by the catalogue rendering
// (explicit vs repository) and the claim truncation rule the renderer applies
// for long claims. The caller is responsible for closing the store via the
// returned cleanup.
//
// Each authority occupies its own scope_value within scope_kind="project"
// so spec §4.2 row 3 (cross-authority dispute marking, enforced in M3 Slice
// 04, commit 34e2411) never fires on this fixture. The explicit seed stays
// at {project, mengdie-test} because the integration tests pin
// ProjectIdentity="mengdie-test" — the agent's first-turn catalogue query
// must resolve to that scope.
func setupMemoryStoreWithSeeds(t *testing.T) *session.SQLiteStore {
	t.Helper()
	directory := t.TempDir()
	store, err := session.OpenSQLite(context.Background(), session.OpenOptions{
		DataDir:     directory,
		ProjectRoot: filepath.Join(directory, "project"),
		Now:         func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("open session store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close session store: %v", err)
		}
	})
	mem := memory.OpenMemory(store)
	seeds := []memory.Memory{
		{
			Claim:     "项目测试入口是 go test ./internal/...",
			Authority: memory.AuthorityExplicit,
			Scope:     memory.Scope{Kind: "project", Value: "mengdie-test"},
			Source:    memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session-seed:1:user"},
		},
		{
			Claim:     "go.mod declares Go 1.26.6",
			Authority: memory.AuthorityRepository,
			Scope:     memory.Scope{Kind: "project", Value: "mengdie-test-2"},
			Source:    memory.SourceRef{Type: memory.SourceTypeFile, Ref: "go.mod:3"},
		},
	}
	for index, seed := range seeds {
		if _, err := mem.Save(context.Background(), seed); err != nil {
			t.Fatalf("seed %d: %v", index, err)
		}
	}
	return store
}

// setupTestRegistry builds a minimal tools.Registry seeded with the M1
// default tools. The integration test does not exercise tool execution, but
// agent.New requires a non-nil Registry.
func setupTestRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	registry, err := tools.NewRegistry(tools.DefaultTools()...)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	return registry
}

// setupTestGuard creates a PathGuard rooted at a per-test temp directory so
// the agent has a real cwd boundary to operate against.
func setupTestGuard(t *testing.T) *platform.PathGuard {
	t.Helper()
	guard, err := platform.NewPathGuard(t.TempDir())
	if err != nil {
		t.Fatalf("build path guard: %v", err)
	}
	return guard
}

// setupTestPolicy builds a headless policy engine rooted at the same temp
// directory as the guard so path comparisons stay consistent. The default
// rules do not allow shell writes in tests; the integration test only needs
// the catalogue path to be reached.
func setupTestPolicy(t *testing.T, root string) *policy.Engine {
	t.Helper()
	engine, err := policy.NewEngine(policy.Options{Root: root, Mode: policy.ModeHeadless})
	if err != nil {
		t.Fatalf("build policy engine: %v", err)
	}
	return engine
}

// setupEmitter returns a run-scoped emitter backed by an in-memory sink. The
// agent emits events synchronously; the sink captures them but the
// integration test does not assert on them.
func setupEmitter(t *testing.T) *events.Emitter {
	t.Helper()
	sink := &events.MemorySink{}
	emitter, err := events.NewEmitter("r1", sink, time.Now)
	if err != nil {
		t.Fatalf("build emitter: %v", err)
	}
	return emitter
}

// requestSystemContent returns the system message content of request, or an
// empty string if the request does not begin with a system message. The
// helper exists because provider.ChatRequest intentionally does not expose a
// SystemContent() method — every agent request is built by
// internal/context/builder.go which pins the system message at index 0.
func requestSystemContent(request provider.ChatRequest) string {
	if len(request.Messages) == 0 {
		return ""
	}
	for _, message := range request.Messages {
		if message.Role == provider.RoleSystem {
			return message.Content
		}
	}
	return ""
}

// stubProvider is a deterministic Provider implementation that returns the
// pre-loaded response at index len(calls) and records every ChatRequest it
// receives so the test can assert on the first-turn request shape.
type stubProvider struct {
	responses []provider.ChatResponse
	calls     []provider.ChatRequest
	index     int
}

func (*stubProvider) ID() string { return "stub" }

func (*stubProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{ToolCalling: true, MaxContextTokens: 4096}, nil
}

func (stub *stubProvider) Stream(ctx context.Context, request provider.ChatRequest, sink provider.StreamSink) (*provider.ChatResponse, error) {
	stub.calls = append(stub.calls, request)
	if stub.index >= len(stub.responses) {
		return nil, errors.New("stub provider: responses exhausted")
	}
	response := stub.responses[stub.index]
	stub.index++
	if response.Message.Content != "" {
		if err := sink.OnEvent(ctx, provider.StreamEvent{Kind: provider.StreamTextDelta, Text: response.Message.Content}); err != nil {
			return nil, err
		}
	}
	return &response, nil
}

// autoApproveBroker returns ApprovalApprove for every Decide call. The
// integration test does not exercise the approval path; the broker is only
// present so authorizer.NewAuthorizer accepts the Options struct.
type autoApproveBroker struct{}

func (autoApproveBroker) Decide(context.Context, policy.ApprovalRequest) (policy.ApprovalResponse, error) {
	return policy.ApprovalResponse{Choice: policy.ApprovalApprove}, nil
}
