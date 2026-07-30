// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var output bytes.Buffer

	if code := run([]string{"--version"}, &output); code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if got := output.String(); !strings.Contains(got, version) {
		t.Fatalf("run() output = %q, want version %q", got, version)
	}
}

func TestRunExplainsDevelopmentStatus(t *testing.T) {
	var output bytes.Buffer

	if code := run(nil, &output); code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if got := output.String(); !strings.Contains(got, "早期开发") {
		t.Fatalf("run() output = %q, want development status", got)
	}
}
