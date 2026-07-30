// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var output bytes.Buffer
	var errors bytes.Buffer

	if code := run([]string{"--version"}, &output, &errors, true); code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if got := output.String(); !strings.Contains(got, version) {
		t.Fatalf("run() output = %q, want version %q", got, version)
	}
	if got := output.String(); !strings.Contains(got, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("run() output = %q, want platform", got)
	}
}

