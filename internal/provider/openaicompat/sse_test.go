// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package openaicompat

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseSSESupportsCommentsCRLFAndMultipleDataLines(t *testing.T) {
	input := "\xef\xbb\xbf: keepalive\r\ndata: {\"value\":\r\ndata: 1}\r\n\r\ndata: [DONE]\r\n\r\n"
	var payloads []string
	done, err := parseSSE(context.Background(), strings.NewReader(input), 1024, func(data []byte) error {
		payloads = append(payloads, string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !done || len(payloads) != 1 || payloads[0] != "{\"value\":\n1}" {
		t.Fatalf("done=%v payloads=%q", done, payloads)
	}
}

func TestParseSSEDiscardsUnterminatedEvent(t *testing.T) {
	called := false
	done, err := parseSSE(context.Background(), strings.NewReader("data: partial"), 1024, func([]byte) error {
		called = true
		return nil
	})
	if err != nil || done || called {
		t.Fatalf("done=%v called=%v err=%v", done, called, err)
	}
}

func TestParseSSEEnforcesEventAndLineLimits(t *testing.T) {
	for _, input := range []string{
		"data: 12345\n\n",
		"data: 12\ndata: 34\n\n",
	} {
		if _, err := parseSSE(context.Background(), strings.NewReader(input), 4, func([]byte) error { return nil }); !errors.Is(err, errSSEEventTooLarge) {
			t.Fatalf("parseSSE() error = %v", err)
		}
	}
}

func TestParseSSEPropagatesContextAndCallbackErrors(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parseSSE(cancelled, strings.NewReader(""), 10, func([]byte) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error = %v", err)
	}
	want := errors.New("stop")
	if _, err := parseSSE(context.Background(), strings.NewReader("data: {}\n\n"), 10, func([]byte) error { return want }); !errors.Is(err, want) {
		t.Fatalf("callback error = %v", err)
	}
}
