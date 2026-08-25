// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

// writeShellSourceFixture writes the smallest passing Go module so a real
// `go test ./...` invocation in the agent's resolved cwd returns success.
// The shell tool emits `events.KindToolCompleted` with Success=true only
// when the underlying process exits 0, so a passing fixture is required
// to exercise the happy-path emit we are asserting against.
func writeShellSourceFixture(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"go.mod":     "module example.com/mengdie-shell-source-fixture\n\ngo 1.26\n",
		"ok.go":      "package fixture\n",
		"ok_test.go": "package fixture\n\nimport \"testing\"\n\nfunc TestPasses(t *testing.T) {}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestShellToolEmitsSourceCommandInToolCompleted drives one Agent.Run whose
// scripted provider emits a single shell tool call (`go test ./...`) and
// then a final assistant message. The test scans the captured event stream
// for the matching `events.KindToolCompleted` payload and asserts that
// `SourceCommand` carries the user's command string verbatim.
//
// This is the contract that lets memory extractor rules (ruleGoTest /
// ruleGoLint) match production shell invocations: the SQLite `events`
// row projection copies `SourceCommand` into `SourceRef`, which the rules
// substring-match against.
func TestShellToolEmitsSourceCommandInToolCompleted(t *testing.T) {
	root := t.TempDir()
	writeShellSourceFixture(t, root)
	fake := &scriptedProvider{responses: []*provider.ChatResponse{
		assistantTool("test", "shell", map[string]any{"command": "go test ./...", "timeout": "2m"}),
		assistantFinal("完成。", provider.Usage{InputTokens: 5, OutputTokens: 3, TotalTokens: 8}),
	}}
	agent, emitter, sink := newAgentTestHarness(t, root, fake, []policy.Rule{
		{
			Name:     "allow-shell",
			Tool:     "shell",
			Effects:  []tools.Effect{tools.EffectExecute},
			Decision: policy.DecisionAllow,
		},
	})
	if _, err := agent.Run(context.Background(), RunRequest{
		RunID: "run-shell-source", Task: "运行测试", Model: "fake:model", MaxTurns: 4, Security: "受控本地执行",
	}, emitter); err != nil {
		t.Fatalf("agent.Run returned error: %v", err)
	}

	var shellCompleted *events.ToolCompleted
	var shellCompletedCount int
	for _, event := range sink.Events() {
		if event.Kind != events.KindToolCompleted {
			continue
		}
		payload, decodeErr := events.DecodePayload[events.ToolCompleted](event)
		if decodeErr != nil {
			t.Fatalf("decode ToolCompleted: %v", decodeErr)
		}
		if payload.Tool != "shell" {
			continue
		}
		shellCompletedCount++
		copy := payload
		shellCompleted = &copy
	}
	if shellCompletedCount == 0 {
		t.Fatalf("no shell ToolCompleted event captured; events=%+v", sink.Events())
	}
	if !shellCompleted.Success {
		t.Fatalf("shell tool did not succeed: payload=%+v", *shellCompleted)
	}
	if shellCompleted.SourceCommand == "" {
		t.Fatalf("shell ToolCompleted.SourceCommand is empty, want %q", "go test ./...")
	}
	if shellCompleted.SourceCommand != "go test ./..." {
		// Allow platform-prefixed argv variants (e.g. "-c go test ./...")
		// while still guaranteeing the user's command survives the round-trip.
		if !strings.Contains(shellCompleted.SourceCommand, "go test ./...") {
			t.Fatalf("shell ToolCompleted.SourceCommand = %q, want %q (or substring match)",
				shellCompleted.SourceCommand, "go test ./...")
		}
	}
}
