// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/brand"
)

func TestRunVersion(t *testing.T) {
	var output bytes.Buffer

	if code := run([]string{"--version"}, &output, true); code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if got := output.String(); !strings.Contains(got, version) {
		t.Fatalf("run() output = %q, want version %q", got, version)
	}
	if got := output.String(); !strings.Contains(got, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("run() output = %q, want platform", got)
	}
	if got := output.String(); strings.Contains(got, brand.Mark) {
		t.Fatalf("run() version output unexpectedly contains banner")
	}
}

func TestRunShowsWelcomeAndDevelopmentStatus(t *testing.T) {
	var output bytes.Buffer

	if code := run(nil, &output, true); code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if got := output.String(); !strings.Contains(got, brand.Mark) {
		t.Fatalf("run() output = %q, want brand mark", got)
	}
	if got := output.String(); !strings.Contains(got, "Agent 功能尚未实现") {
		t.Fatalf("run() output = %q, want development status", got)
	}
	for _, label := range []string{"构建", "平台", "项目", "模型", "安全"} {
		if got := output.String(); !strings.Contains(got, label) {
			t.Errorf("run() output = %q, want label %q", got, label)
		}
	}
}

func TestRunOmitsWelcomeWhenOutputIsRedirected(t *testing.T) {
	var output bytes.Buffer

	if code := run(nil, &output, false); code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if got := output.String(); strings.Contains(got, brand.Mark) {
		t.Fatalf("run() redirected output unexpectedly contains banner")
	}
	if got := output.String(); !strings.Contains(got, "Agent 功能尚未实现") {
		t.Fatalf("run() output = %q, want development status", got)
	}
}
