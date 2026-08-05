// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTextBrokerParsesChineseAndEnglishChoices(t *testing.T) {
	for input, want := range map[string]ApprovalChoice{
		"y\n":    ApprovalApprove,
		"允许\n":   ApprovalApprove,
		"no\n":   ApprovalReject,
		"拒绝\n":   ApprovalReject,
		"edit\n": ApprovalEdit,
		"编辑\n":   ApprovalEdit,
	} {
		t.Run(strings.TrimSpace(input), func(t *testing.T) {
			var output strings.Builder
			broker, err := NewTextBroker(strings.NewReader(input), &output)
			if err != nil {
				t.Fatal(err)
			}
			response, err := broker.Decide(context.Background(), ApprovalRequest{Prompt: "允许？", Risk: "低"})
			if err != nil || response.Choice != want {
				t.Fatalf("response=%+v error=%v want=%q", response, err, want)
			}
			if !strings.Contains(output.String(), "风险：低") {
				t.Fatalf("prompt = %q", output.String())
			}
		})
	}
}

func TestTextBrokerRetriesAndBoundsInput(t *testing.T) {
	var output strings.Builder
	broker, _ := NewTextBroker(strings.NewReader("maybe\ny\n"), &output)
	response, err := broker.Decide(context.Background(), ApprovalRequest{Prompt: "允许？", Risk: "中"})
	if err != nil || response.Choice != ApprovalApprove || !strings.Contains(output.String(), "请输入") {
		t.Fatalf("response=%+v error=%v output=%q", response, err, output.String())
	}

	broker, _ = NewTextBroker(strings.NewReader(strings.Repeat("x", defaultApprovalInputBytes+1)+"\n"), &strings.Builder{})
	if _, err := broker.Decide(context.Background(), ApprovalRequest{}); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestTextBrokerHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	broker, _ := NewTextBroker(strings.NewReader("y\n"), &strings.Builder{})
	if _, err := broker.Decide(ctx, ApprovalRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
