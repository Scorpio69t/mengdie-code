// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/policy"
)

func TestApprovalBrokerReturnsOneExactDecision(t *testing.T) {
	broker := NewApprovalBroker()
	t.Cleanup(broker.Close)
	request := policy.ApprovalRequest{CallID: "call-1", Tool: "edit_file", Risk: "medium"}
	type outcome struct {
		response policy.ApprovalResponse
		err      error
	}
	result := make(chan outcome, 1)
	go func() {
		response, err := broker.Decide(context.Background(), request)
		result <- outcome{response: response, err: err}
	}()

	prompt := receiveApprovalPrompt(t, broker.Prompts())
	if !reflect.DeepEqual(prompt.Request, request) {
		t.Fatalf("prompt request = %+v, want %+v", prompt.Request, request)
	}
	if !prompt.Resolve(policy.ApprovalResponse{Choice: policy.ApprovalApprove}) {
		t.Fatal("first Resolve() = false, want true")
	}
	if prompt.Resolve(policy.ApprovalResponse{Choice: policy.ApprovalReject}) {
		t.Fatal("second Resolve() = true, want false")
	}

	select {
	case got := <-result:
		if got.err != nil || got.response.Choice != policy.ApprovalApprove {
			t.Fatalf("Decide() = (%+v, %v)", got.response, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Decide() did not return")
	}
}

func TestApprovalBrokerCancellationAndCloseUnblockWaiters(t *testing.T) {
	t.Run("context cancellation", func(t *testing.T) {
		broker := NewApprovalBroker()
		defer broker.Close()
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := broker.Decide(ctx, policy.ApprovalRequest{CallID: "call-cancel"})
			result <- err
		}()
		prompt := receiveApprovalPrompt(t, broker.Prompts())
		cancel()
		if err := receiveApprovalError(t, result); !errors.Is(err, context.Canceled) {
			t.Fatalf("Decide() error = %v, want context.Canceled", err)
		}
		if prompt.Resolve(policy.ApprovalResponse{Choice: policy.ApprovalApprove}) {
			t.Fatal("cancelled prompt was resolved")
		}
	})

	t.Run("broker close", func(t *testing.T) {
		broker := NewApprovalBroker()
		result := make(chan error, 1)
		go func() {
			_, err := broker.Decide(context.Background(), policy.ApprovalRequest{CallID: "call-close"})
			result <- err
		}()
		_ = receiveApprovalPrompt(t, broker.Prompts())
		broker.Close()
		if err := receiveApprovalError(t, result); err == nil {
			t.Fatal("Decide() error = nil after Close()")
		}
		if _, open := <-broker.Prompts(); open {
			t.Fatal("prompt stream remained open after Close()")
		}
	})
}

func receiveApprovalPrompt(t *testing.T, prompts <-chan ApprovalPrompt) ApprovalPrompt {
	t.Helper()
	select {
	case prompt := <-prompts:
		return prompt
	case <-time.After(time.Second):
		t.Fatal("approval prompt not received")
		return ApprovalPrompt{}
	}
}

func receiveApprovalError(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("approval decision did not unblock")
		return nil
	}
}
