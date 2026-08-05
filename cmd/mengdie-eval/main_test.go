// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestRunReturnsRunErrorWhenDiagnosticWriteFails(t *testing.T) {
	want := errors.New("diagnostic writer failed")
	code := run(context.Background(), []string{"unexpected"}, io.Discard, failingWriter{err: want})
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
}

type failingWriter struct {
	err error
}

func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
