// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/config"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

// TestRunAgentRegistersMemoryRecallTool verifies the slice 02 §10 wiring
// contract: once app.Runtime builds the default Agent tool set inside
// runAgent, the resulting ChatRequest must advertise the memory_recall
// tool so the model can opt into Tier 3 atomic recall during the run.
//
// Before the slice 02 wiring lands, runAgent calls tools.DefaultTools()
// with no options, so memory_recall is NOT in the registry and this
// test fails with "memory_recall not in registry"; after the wiring
// patch the test passes. The test drives one full `mengdie exec` with
// a deterministic fake provider so the ChatRequest the agent builds
// off a.registry.Specs() can be inspected via fake.requests[0].Tools.
// MaxTurns is pinned by writeRuntimeConfig's profile, the script
// provider replies with a final assistant message on the first turn,
// and the test never exercises the tool itself — the assertion is on
// registration, not execution.
func TestRunAgentRegistersMemoryRecallTool(t *testing.T) {
	root := t.TempDir()
	writeRuntimeConfig(t, root)
	application, _, _ := newTestApp(t, nil)
	fake := &appFakeProvider{responses: []*provider.ChatResponse{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}
	application.newProvider = func(config.Profile, string) (provider.Provider, error) {
		return fake, nil
	}

	if code := application.Run(context.Background(),
		[]string{"exec", "--cwd", root, "--json", "probe memory_recall"}, false,
	); code != ExitOK {
		t.Fatalf("Run() code = %d, want ExitOK", code)
	}

	if len(fake.requests) == 0 {
		t.Fatal("provider was never called; runAgent pipeline is broken")
	}
	found := false
	names := make([]string, 0, len(fake.requests[0].Tools))
	for _, tool := range fake.requests[0].Tools {
		if tool.Function.Name == "memory_recall" {
			found = true
		}
		names = append(names, tool.Function.Name)
	}
	if !found {
		t.Fatalf("memory_recall not in ChatRequest.Tools; got: %s", strings.Join(names, ", "))
	}
}
