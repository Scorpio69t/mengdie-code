// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"errors"
	"sync"

	"github.com/Scorpio69t/mengdie-code/internal/policy"
)

// ApprovalPrompt is one exact prepared-call decision waiting for local user
// input. Resolve returns false when the run has already been cancelled.
type ApprovalPrompt struct {
	Request  policy.ApprovalRequest
	response chan policy.ApprovalResponse
	done     <-chan struct{}
	once     *sync.Once
}

func (prompt ApprovalPrompt) active() bool {
	if prompt.done == nil {
		return false
	}
	select {
	case <-prompt.done:
		return false
	default:
		return true
	}
}

func (prompt ApprovalPrompt) Resolve(response policy.ApprovalResponse) bool {
	if prompt.once == nil || prompt.response == nil || prompt.done == nil {
		return false
	}
	select {
	case <-prompt.done:
		return false
	default:
	}
	resolved := false
	prompt.once.Do(func() {
		select {
		case <-prompt.done:
			return
		default:
		}
		select {
		case prompt.response <- response:
			resolved = true
		case <-prompt.done:
		}
	})
	return resolved
}

// ApprovalSource is the minimal decision channel consumed by the TUI model.
type ApprovalSource interface {
	Prompts() <-chan ApprovalPrompt
	Close()
}

// ApprovalBroker bridges the blocking Policy Broker contract to Bubble Tea's
// message loop. It only returns user choices; Capability issuance remains in
// policy.Authorizer.
type ApprovalBroker struct {
	mu       sync.Mutex
	prompts  chan ApprovalPrompt
	closed   chan struct{}
	closeOne sync.Once
}

func NewApprovalBroker() *ApprovalBroker {
	return &ApprovalBroker{prompts: make(chan ApprovalPrompt), closed: make(chan struct{})}
}

func (broker *ApprovalBroker) Decide(ctx context.Context, request policy.ApprovalRequest) (policy.ApprovalResponse, error) {
	if broker == nil {
		return policy.ApprovalResponse{}, errors.New("tui approval broker is required")
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	select {
	case <-broker.closed:
		return policy.ApprovalResponse{}, errors.New("tui approval broker is closed")
	default:
	}
	response := make(chan policy.ApprovalResponse, 1)
	done := make(chan struct{})
	defer close(done)
	prompt := ApprovalPrompt{Request: request, response: response, done: done, once: &sync.Once{}}
	select {
	case broker.prompts <- prompt:
	case <-ctx.Done():
		return policy.ApprovalResponse{}, ctx.Err()
	case <-broker.closed:
		return policy.ApprovalResponse{}, errors.New("tui approval broker is closed")
	}
	select {
	case decision := <-response:
		return decision, nil
	case <-ctx.Done():
		return policy.ApprovalResponse{}, ctx.Err()
	case <-broker.closed:
		return policy.ApprovalResponse{}, errors.New("tui approval broker is closed")
	}
}

func (broker *ApprovalBroker) Prompts() <-chan ApprovalPrompt {
	if broker == nil {
		return nil
	}
	return broker.prompts
}

func (broker *ApprovalBroker) Close() {
	if broker == nil {
		return
	}
	broker.closeOne.Do(func() {
		close(broker.closed)
		broker.mu.Lock()
		close(broker.prompts)
		broker.mu.Unlock()
	})
}

var _ policy.Broker = (*ApprovalBroker)(nil)
