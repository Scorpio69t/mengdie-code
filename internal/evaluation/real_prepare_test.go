// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package evaluation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fakeGitExecutor struct {
	calls       []GitInvocation
	commit      string
	status      string
	failCommand string
	cancel      context.CancelFunc
	cancelOn    string
}

func (fake *fakeGitExecutor) Run(ctx context.Context, invocation GitInvocation) (GitOutput, error) {
	fake.calls = append(fake.calls, invocation)
	joined := strings.Join(invocation.Args, " ")
	if hasArgument(invocation.Args, fake.cancelOn) && fake.cancel != nil {
		fake.cancel()
	}
	if fake.failCommand != "" && hasArgument(invocation.Args, fake.failCommand) {
		return GitOutput{Stderr: "private remote diagnostics"}, errors.New("git helper error with sensitive details")
	}
	if strings.Contains(joined, "rev-parse") {
		return GitOutput{Stdout: fake.commit + "\n"}, nil
	}
	if strings.Contains(joined, "status") {
		return GitOutput{Stdout: fake.status}, nil
	}
	return GitOutput{}, nil
}

func hasArgument(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func TestPrepareRealRepositoryTaskPinsCommitAndReturnsPrivateWorkspace(t *testing.T) {
	manifest := validRealManifest()
	fake := &fakeGitExecutor{commit: manifest.Tasks[0].SourceCommit}
	t.Setenv("MENGDIE_TEST_SECRET", "must-not-reach-git")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7897")

	prepared, err := prepareRealRepositoryTask(context.Background(), manifest, "gin-2121", fake)
	if err != nil {
		t.Fatalf("prepareRealRepositoryTask() error = %v", err)
	}
	if len(fake.calls) != 5 {
		t.Fatalf("Git call count = %d, want 5", len(fake.calls))
	}
	if prepared.SourceCommit != manifest.Tasks[0].SourceCommit {
		t.Fatalf("SourceCommit = %q", prepared.SourceCommit)
	}
	info, err := os.Stat(filepath.Dir(prepared.Path))
	if err != nil {
		t.Fatalf("stat temporary root: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("temporary root mode = %04o, want 0700", info.Mode().Perm())
	}
	if got := filepath.Base(prepared.Path); got != "workspace" {
		t.Fatalf("workspace path base = %q, want workspace", got)
	}

	fetch := fake.calls[1]
	fetchArgs := strings.Join(fetch.Args, " ")
	for _, expected := range []string{"fetch", "--no-tags", "--no-recurse-submodules", "--depth=1", manifest.Tasks[0].SourceURL, manifest.Tasks[0].SourceCommit} {
		if !strings.Contains(fetchArgs, expected) {
			t.Errorf("fetch args %q missing %q", fetchArgs, expected)
		}
	}
	for _, invocation := range fake.calls {
		if strings.Contains(strings.Join(invocation.Args, " "), "credential.helper=") == false {
			t.Errorf("Git invocation missing credential.helper reset: %q", invocation.Args)
		}
		if envValue(invocation.Environment, "GIT_TERMINAL_PROMPT") != "0" || envValue(invocation.Environment, "GIT_ALLOW_PROTOCOL") != "https" {
			t.Errorf("unsafe Git environment: %v", invocation.Environment)
		}
		if envValue(invocation.Environment, "MENGDIE_TEST_SECRET") != "" {
			t.Errorf("unrelated environment secret propagated to Git")
		}
		if envValue(invocation.Environment, "HTTPS_PROXY") != "http://127.0.0.1:7897" {
			t.Errorf("configured HTTPS proxy not preserved")
		}
	}
	if err := prepared.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace still exists after Close(): err = %v", err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestPrepareRealRepositoryTaskCleansUpOnFailure(t *testing.T) {
	manifest := validRealManifest()
	fake := &fakeGitExecutor{commit: manifest.Tasks[0].SourceCommit, failCommand: "fetch"}
	prepared, err := prepareRealRepositoryTask(context.Background(), manifest, "gin-2121", fake)
	if err == nil || prepared != nil || !strings.Contains(err.Error(), "Git fetch failed") {
		t.Fatalf("prepareRealRepositoryTask() = (%v, %v), want redacted Git fetch error", prepared, err)
	}
	if strings.Contains(err.Error(), "private remote diagnostics") || strings.Contains(err.Error(), "sensitive details") {
		t.Fatalf("prepare error leaked Git diagnostics: %v", err)
	}
	if len(fake.calls) < 2 {
		t.Fatalf("Git calls = %d, want init and fetch", len(fake.calls))
	}
	root := filepath.Dir(fake.calls[0].Directory)
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary root still exists after failure: err = %v", err)
	}
}

func TestPrepareRealRepositoryTaskRejectsCommitMismatchAndDirtyCheckout(t *testing.T) {
	tests := []struct {
		name   string
		commit string
		status string
		want   string
	}{
		{name: "commit mismatch", commit: strings.Repeat("0", 40), want: "did not match"},
		{name: "dirty checkout", commit: validRealManifest().Tasks[0].SourceCommit, status: "?? unexpected", want: "not clean"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validRealManifest()
			fake := &fakeGitExecutor{commit: test.commit, status: test.status}
			prepared, err := prepareRealRepositoryTask(context.Background(), manifest, "gin-2121", fake)
			if err == nil || prepared != nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("prepareRealRepositoryTask() = (%v, %v), want %q error", prepared, err, test.want)
			}
			root := filepath.Dir(fake.calls[0].Directory)
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary root still exists after rejection: err = %v", err)
			}
		})
	}
}

func TestPrepareRealRepositoryTaskRestrictsGitHubAndCancellation(t *testing.T) {
	manifest := validRealManifest()
	fake := &fakeGitExecutor{commit: manifest.Tasks[0].SourceCommit}
	manifest.Tasks[0].SourceURL = "https://example.com/org/repo.git"
	if _, err := prepareRealRepositoryTask(context.Background(), manifest, "gin-2121", fake); err == nil || len(fake.calls) != 0 {
		t.Fatalf("non-GitHub source should be rejected before Git calls; calls=%d err=%v", len(fake.calls), err)
	}

	manifest = validRealManifest()
	ctx, cancel := context.WithCancel(context.Background())
	fake = &fakeGitExecutor{commit: manifest.Tasks[0].SourceCommit, cancel: cancel, cancelOn: "fetch"}
	if _, err := prepareRealRepositoryTask(ctx, manifest, "gin-2121", fake); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled preparation error = %v, want cancellation", err)
	}
	root := filepath.Dir(fake.calls[0].Directory)
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary root still exists after cancellation: err = %v", err)
	}
}

func TestValidateFetchHostRejectsCredentialsAndURLModifiers(t *testing.T) {
	for _, source := range []string{
		"https://user:token@github.com/org/repo.git",
		"https://github.com/org/repo.git?token=value",
		"https://github.com/org/repo.git#fragment",
		"https://github.com:8443/org/repo.git",
	} {
		t.Run(source, func(t *testing.T) {
			if err := validateFetchHost(source); err == nil {
				t.Fatalf("validateFetchHost(%q) unexpectedly succeeded", source)
			}
		})
	}
}

func envValue(environment []string, key string) string {
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, key) {
			return value
		}
	}
	return ""
}
