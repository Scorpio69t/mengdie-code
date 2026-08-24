// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/agent"
	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

// TestAgentFirstTurnReceivesMemoryCatalogue verifies the spec §6.2 first-turn
// injection contract: when an Agent is constructed with a MemoryRetriever and
// a ProjectIdentity, the very first chat request the provider receives must
// carry a memory catalogue section appended to the system message. The test
// does not exercise any tool execution; MaxTurns is pinned to 1 so the
// assistant returns immediately after the seeded response and the assertion
// can inspect stub.calls[0].
//
// The catalogue section is identified by its header — the brief pins a
// markdown bullet list whose first line carries the Chinese project-memories
// header and the literal word "memory" / "记忆". The test asserts on the
// header plus one of the seeded claim strings so a regression that injects
// the section but drops the body content still surfaces here.
func TestAgentFirstTurnReceivesMemoryCatalogue(t *testing.T) {
	store := setupMemoryStoreWithSeeds(t)
	retriever := agent.NewRetrieverAdapter(memory.NewRetriever(memory.OpenMemory(store)))
	stub := &stubProvider{responses: []provider.ChatResponse{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}
	guard := setupTestGuard(t)
	runtime, err := agent.New(agent.Options{
		Provider:         stub,
		Registry:         setupTestRegistry(t),
		Guard:            guard,
		Policy:           setupTestPolicy(t, guard.Root()),
		Broker:           autoApproveBroker{},
		Now:              time.Now,
		MaxContextTokens: 4096,
		MemoryRetriever:  retriever,
		ProjectIdentity:  "mengdie-test",
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		RunID:        "r1",
		Task:         "测试",
		Model:        "stub",
		DisplayModel: "stub",
		MaxTurns:     1,
		Security:     "controlled",
	}, setupEmitter(t))
	if err != nil {
		t.Fatalf("agent.Run: %v", err)
	}
	if result.Summary != "done" {
		t.Fatalf("result.Summary = %q, want %q", result.Summary, "done")
	}
	if len(stub.calls) == 0 {
		t.Fatal("provider was not called")
	}
	systemContent := requestSystemContent(stub.calls[0])
	if !strings.Contains(systemContent, "项目记忆") {
		t.Fatalf("first-turn system message must include the project-memory catalogue header; got %q", systemContent)
	}
	if !strings.Contains(systemContent, "go test ./internal/...") {
		t.Fatalf("first-turn system message must include the seeded explicit claim; got %q", systemContent)
	}
}

// TestAgentFirstTurnSkipsInjectionOnResume verifies spec §6.2 + brief
// guidance that a resumed run does NOT re-inject the catalogue. The
// Recovery != nil branch in Agent.Run is the only path where injection is
// skipped, so we drive a single-turn recovery that returns immediately.
func TestAgentFirstTurnSkipsInjectionOnResume(t *testing.T) {
	store := setupMemoryStoreWithSeeds(t)
	retriever := agent.NewRetrieverAdapter(memory.NewRetriever(memory.OpenMemory(store)))
	stub := &stubProvider{responses: []provider.ChatResponse{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "resumed"}},
	}}
	guard := setupTestGuard(t)
	runtime, err := agent.New(agent.Options{
		Provider:         stub,
		Registry:         setupTestRegistry(t),
		Guard:            guard,
		Policy:           setupTestPolicy(t, guard.Root()),
		Broker:           autoApproveBroker{},
		Now:              time.Now,
		MaxContextTokens: 4096,
		MemoryRetriever:  retriever,
		ProjectIdentity:  "mengdie-test",
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		RunID:        "r1",
		Task:         "继续",
		Model:        "stub",
		DisplayModel: "stub",
		MaxTurns:     1,
		Security:     "controlled",
		Recovery: &agent.RecoveryAction{
			SourceRunID: "run-old",
			Kind:        agent.RecoveryVerifyWrite,
			Call: provider.ToolCall{
				ID:        "noop-write",
				Type:      "function",
				Name:      "write_file",
				Arguments: []byte(`{"path":"a.txt","content":"x","overwrite":true}`),
			},
		},
	}, setupEmitter(t))
	if err != nil {
		t.Fatalf("agent.Run: %v", err)
	}
	if result.Summary != "resumed" {
		t.Fatalf("result.Summary = %q, want %q", result.Summary, "resumed")
	}
	if len(stub.calls) == 0 {
		t.Fatal("provider was not called")
	}
	systemContent := requestSystemContent(stub.calls[0])
	if strings.Contains(systemContent, "项目记忆") {
		t.Fatalf("resumed run must not re-inject the catalogue header; got %q", systemContent)
	}
}

// ensureCompileTimeReferences pins the helper symbols this test depends on so
// future refactors that rename or drop them surface as a compile-time error
// instead of a confusing test runtime failure.
var (
	_ = memory.NewRetriever
	_ = requestSystemContent
)
