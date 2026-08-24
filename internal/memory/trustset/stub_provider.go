// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package trustset

// stubProvider is a deterministic provider.Provider implementation that
// emits the pre-canned response as a single text delta and then finishes.
// It exists so the runner can drive the LLM / Hybrid extractor without
// depending on real network code (Task 9 specifies that 4 of the 5 new
// scenarios are LLM-free and only the llm_tool_pref / hybrid-both scenarios
// need an LLM at all). The same shape is reused by internal/memory/
// extractor/llm_test.go but lives in a different package so the runner
// stays independent of test helpers.
//
// The struct is intentionally tiny: response is the entire upstream output
// (one or more JSON Lines candidates, one per line); err (always nil in
// production usage) is reserved for future failure-injection tests.

import (
	"context"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

type stubProvider struct {
	response string
}

func (s *stubProvider) ID() string { return "trustset-stub" }

func (s *stubProvider) Capabilities(_ context.Context, _ string) (provider.Capabilities, error) {
	return provider.Capabilities{ToolCalling: true, MaxContextTokens: 4096}, nil
}

func (s *stubProvider) Stream(ctx context.Context, _ provider.ChatRequest, sink provider.StreamSink) (*provider.ChatResponse, error) {
	if s == nil {
		return nil, nil
	}
	if sink != nil && s.response != "" {
		if err := sink.OnEvent(ctx, provider.StreamEvent{Kind: provider.StreamTextDelta, Text: s.response}); err != nil {
			return nil, err
		}
	}
	return &provider.ChatResponse{
		Message: provider.Message{Role: provider.RoleAssistant, Content: s.response},
	}, nil
}
