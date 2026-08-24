// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/cost"
	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

// RunState is deliberately in-memory in M1. It contains only one active run
// and is not a persistence or resume contract.
type RunState struct {
	mu                    sync.RWMutex
	RunID                 string
	Messages              []provider.Message
	CompactionMessages    []provider.Message
	Summary               string
	Todos                 []tools.Todo
	Turn                  int
	Usage                 provider.Usage
	RequestCount          int64
	UsageReportedRequests int64
	EstimatedCostPicoUSD  int64
	EstimatedCostRequests int64
	UnknownCostRequests   int64
	DeniedTools           int
	StartedAt             time.Time
}

func (s *RunState) snapshot() ([]provider.Message, []provider.Message, []tools.Todo, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	messages := cloneMessages(s.Messages)
	compactionMessages := cloneMessages(s.CompactionMessages)
	todos := append([]tools.Todo(nil), s.Todos...)
	return messages, compactionMessages, todos, s.Summary
}

func (s *RunState) applyCompaction(messages, sourceMessages []provider.Message, summary string) {
	s.mu.Lock()
	s.Messages = cloneMessages(messages)
	s.CompactionMessages = cloneMessages(sourceMessages)
	s.Summary = summary
	s.mu.Unlock()
}

func cloneMessages(messages []provider.Message) []provider.Message {
	result := make([]provider.Message, len(messages))
	for index, message := range messages {
		result[index] = message
		result[index].ToolCalls = make([]provider.ToolCall, len(message.ToolCalls))
		for callIndex, call := range message.ToolCalls {
			result[index].ToolCalls[callIndex] = call
			result[index].ToolCalls[callIndex].Arguments = append([]byte(nil), call.Arguments...)
		}
	}
	return result
}

func (s *RunState) appendMessage(message provider.Message) {
	s.mu.Lock()
	s.Messages = append(s.Messages, message)
	s.CompactionMessages = append(s.CompactionMessages, cloneMessages([]provider.Message{message})[0])
	s.mu.Unlock()
}

func (s *RunState) appendMessageWithCompactionSource(message, source provider.Message) {
	s.mu.Lock()
	s.Messages = append(s.Messages, message)
	s.CompactionMessages = append(s.CompactionMessages, cloneMessages([]provider.Message{source})[0])
	s.mu.Unlock()
}

func (s *RunState) replaceTodos(todos []tools.Todo) {
	s.mu.Lock()
	s.Todos = append([]tools.Todo(nil), todos...)
	s.mu.Unlock()
}

func (s *RunState) addUsage(usage provider.Usage, fact events.UsageUpdated) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	input, ok := addNonNegative(s.Usage.InputTokens, usage.InputTokens)
	if !ok {
		return errors.New("agent: input token total overflow")
	}
	output, ok := addNonNegative(s.Usage.OutputTokens, usage.OutputTokens)
	if !ok {
		return errors.New("agent: output token total overflow")
	}
	total, ok := addNonNegative(s.Usage.TotalTokens, usage.TotalTokens)
	if !ok {
		return errors.New("agent: token total overflow")
	}
	cacheRead, ok := addNonNegative(s.Usage.CacheReadTokens, usage.CacheReadTokens)
	if !ok {
		return errors.New("agent: cache-read token total overflow")
	}
	requests, ok := addNonNegative(s.RequestCount, fact.RequestCount)
	if !ok {
		return errors.New("agent: request total overflow")
	}
	estimatedCost := s.EstimatedCostPicoUSD
	if fact.CostStatus == cost.StatusEstimated {
		estimatedCost, ok = addNonNegative(estimatedCost, fact.EstimatedCostPicoUSD)
		if !ok {
			return errors.New("agent: estimated cost total overflow")
		}
	}
	s.Usage = provider.Usage{InputTokens: input, OutputTokens: output, TotalTokens: total, CacheReadTokens: cacheRead}
	s.RequestCount = requests
	if fact.UsageReported {
		s.UsageReportedRequests += fact.RequestCount
	}
	if fact.CostStatus == cost.StatusEstimated {
		s.EstimatedCostPicoUSD = estimatedCost
		s.EstimatedCostRequests += fact.RequestCount
	} else {
		s.UnknownCostRequests += fact.RequestCount
	}
	return nil
}

func addNonNegative(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}

func (s *RunState) result(summary string) RunResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return RunResult{
		Summary: summary, Turns: s.Turn, Usage: s.Usage,
		RequestCount: s.RequestCount, UsageReportedRequests: s.UsageReportedRequests,
		EstimatedCostPicoUSD: s.EstimatedCostPicoUSD, EstimatedCostRequests: s.EstimatedCostRequests,
		UnknownCostRequests: s.UnknownCostRequests,
		Todos:               append([]tools.Todo(nil), s.Todos...), DeniedTools: s.DeniedTools,
	}
}

func (s *RunState) recordDenial() {
	s.mu.Lock()
	s.DeniedTools++
	s.mu.Unlock()
}
