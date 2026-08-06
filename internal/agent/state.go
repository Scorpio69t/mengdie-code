// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"sync"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

// RunState is deliberately in-memory in M1. It contains only one active run
// and is not a persistence or resume contract.
type RunState struct {
	mu          sync.RWMutex
	RunID       string
	Messages    []provider.Message
	Todos       []tools.Todo
	Turn        int
	Usage       provider.Usage
	DeniedTools int
	StartedAt   time.Time
}

func (s *RunState) snapshot() ([]provider.Message, []tools.Todo) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	messages := append([]provider.Message(nil), s.Messages...)
	todos := append([]tools.Todo(nil), s.Todos...)
	return messages, todos
}

func (s *RunState) appendMessage(message provider.Message) {
	s.mu.Lock()
	s.Messages = append(s.Messages, message)
	s.mu.Unlock()
}

func (s *RunState) replaceTodos(todos []tools.Todo) {
	s.mu.Lock()
	s.Todos = append([]tools.Todo(nil), todos...)
	s.mu.Unlock()
}

func (s *RunState) setUsage(usage provider.Usage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Usage == usage {
		return false
	}
	s.Usage = usage
	return true
}

func (s *RunState) result(summary string) RunResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return RunResult{
		Summary: summary, Turns: s.Turn, Usage: s.Usage,
		Todos: append([]tools.Todo(nil), s.Todos...), DeniedTools: s.DeniedTools,
	}
}

func (s *RunState) recordDenial() {
	s.mu.Lock()
	s.DeniedTools++
	s.mu.Unlock()
}
