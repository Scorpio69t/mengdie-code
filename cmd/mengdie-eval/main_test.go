// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRunRejectsUnknownSubcommand(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := run(context.Background(), []string{"unknown"}, io.Discard, stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown subcommand") {
		t.Fatalf("stderr missing unknown subcommand message: %q", stderr.String())
	}
}

func TestRunRejectsBaselinePositionalArguments(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := run(context.Background(), []string{"baseline", "--manifest", "x", "extra"}, io.Discard, stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
}

func TestRunRejectsChaosPositionalArguments(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := run(context.Background(), []string{"chaos", "--manifest", "x", "extra"}, io.Discard, stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
}

func TestRunReturnsRunErrorWhenDiagnosticWriteFails(t *testing.T) {
	want := errors.New("diagnostic writer failed")
	code := run(context.Background(), []string{"baseline", "--manifest", "evals/coding/missing.json"}, io.Discard, failingWriter{err: want})
	if code != 1 && code != 2 {
		t.Fatalf("run() code = %d, want 1 or 2", code)
	}
}

type failingWriter struct {
	err error
}

func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
