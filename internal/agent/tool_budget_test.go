// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

func TestToolBudgetStopsBeforeSecondCallAndCountsDenial(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "value.txt"), []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &scriptedProvider{responses: []*provider.ChatResponse{
		assistantTool("denied", "write_file", map[string]any{"path": "value.txt", "content": "bad", "overwrite": true}),
		assistantTool("read", "read_file", map[string]any{"path": "value.txt"}),
	}}
	runtime, emitter, sink := newAgentTestHarness(t, root, fake, nil)
	result, err := runtime.Run(context.Background(), RunRequest{RunID: "run-test", Task: "bounded", Model: "fake:model", MaxTurns: 4,
		ToolBudget: &ToolBudget{MaxToolCalls: 1, MaxCommandCalls: 0}}, emitter)
	if !errors.Is(err, ErrToolBudgetExceeded) || result.ToolCalls != 1 || result.DeniedTools != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	proposed := 0
	for _, event := range sink.Events() {
		if event.Kind == events.KindToolProposed {
			proposed++
		}
	}
	if proposed != 1 {
		t.Fatalf("proposed calls=%d, want 1", proposed)
	}
	content, _ := os.ReadFile(filepath.Join(root, "value.txt"))
	if string(content) != "value" {
		t.Fatalf("denied tool changed file: %q", content)
	}
}

func TestToolBudgetRejectsCommandBeforePrepare(t *testing.T) {
	root := t.TempDir()
	fake := &scriptedProvider{responses: []*provider.ChatResponse{
		assistantTool("shell", "shell", map[string]any{"command": "go test ./..."}),
	}}
	runtime, emitter, sink := newAgentTestHarness(t, root, fake, nil)
	result, err := runtime.Run(context.Background(), RunRequest{RunID: "run-test", Task: "bounded", Model: "fake:model", MaxTurns: 2,
		ToolBudget: &ToolBudget{MaxToolCalls: 1, MaxCommandCalls: 0}}, emitter)
	if !errors.Is(err, ErrCommandBudgetExceeded) || result.ToolCalls != 0 || result.CommandCalls != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, event := range sink.Events() {
		if event.Kind == events.KindToolProposed || event.Kind == events.KindToolStarted {
			t.Fatalf("command crossed execution boundary: %s", event.Kind)
		}
	}
}

func TestToolBudgetReservationIsAtomic(t *testing.T) {
	state := &RunState{}
	budget := &ToolBudget{MaxToolCalls: 20, MaxCommandCalls: 5}
	var workers sync.WaitGroup
	for i := 0; i < 100; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_ = state.reserveToolCall("shell", budget)
		}()
	}
	workers.Wait()
	if state.ToolCalls != 5 || state.CommandCalls != 5 {
		t.Fatalf("reservations=(%d,%d), want (5,5)", state.ToolCalls, state.CommandCalls)
	}
}
