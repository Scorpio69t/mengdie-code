// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

func TestRealBaselineVerifierHelper(t *testing.T) {
	mode, err := os.ReadFile("baseline-helper-mode")
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(mode), "\n", 2)
	switch parts[0] {
	case "success":
		_, _ = fmt.Fprint(os.Stdout, "baseline output\n")
	case "failure":
		_, _ = fmt.Fprint(os.Stderr, "private verifier detail\n")
		os.Exit(7)
	case "large-output":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", realBaselineStreamLimit+1024))
	case "sleep":
		time.Sleep(5 * time.Second)
	case "child":
		if len(parts) != 2 {
			t.Fatal("child mode requires marker path")
		}
		if os.Getenv("MENGDIE_BASELINE_CHILD") == "1" {
			time.Sleep(1500 * time.Millisecond)
			_ = os.WriteFile(parts[1], []byte("escaped"), 0o600)
			return
		}
		child := exec.Command(os.Args[0], "-test.run=^TestRealBaselineVerifierHelper$")
		child.Env = append(os.Environ(), "MENGDIE_BASELINE_CHILD=1")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(parts[1]+"-started", []byte("started"), 0o600)
		time.Sleep(5 * time.Second)
	default:
		t.Fatalf("unknown baseline helper mode %q", mode)
	}
}

func baselineHelperManifest(t *testing.T, timeout string, expected int) (RealRepositoryManifest, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manifest := validRealManifest()
	manifest.Tasks[0].Verifier = VerifySpec{
		Command: []string{filepath.Base(executable), "-test.run=^TestRealBaselineVerifierHelper$"},
		Timeout: timeout,
	}
	manifest.Tasks[0].Baseline.ExpectedExitCode = expected
	return manifest, executable
}

func baselineFakePrepare(t *testing.T, mode string) (realBaselinePrepare, *string) {
	t.Helper()
	root := ""
	prepare := func(_ context.Context, manifest RealRepositoryManifest, taskID string) (*PreparedRealRepository, error) {
		var err error
		root, err = os.MkdirTemp(t.TempDir(), "baseline-")
		if err != nil {
			return nil, err
		}
		workspace := filepath.Join(root, "workspace")
		if err := os.Mkdir(workspace, 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(workspace, "baseline-helper-mode"), []byte(mode), 0o600); err != nil {
			return nil, err
		}
		return &PreparedRealRepository{Path: workspace, SourceCommit: manifest.Tasks[0].SourceCommit, root: root}, nil
	}
	return prepare, &root
}

func TestRealRepositoryBaselineMatchedAndMismatched(t *testing.T) {
	for _, test := range []struct {
		name, mode, wantStatus string
		expected, actual       int
	}{
		{name: "expected failure", mode: "failure", expected: 7, actual: 7, wantStatus: "baseline_matched"},
		{name: "unexpected success", mode: "success", expected: 7, actual: 0, wantStatus: "baseline_mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest, executable := baselineHelperManifest(t, "5s", test.expected)
			prepare, root := baselineFakePrepare(t, test.mode)
			result, err := runRealRepositoryBaseline(context.Background(), manifest, manifest.Tasks[0].ID,
				RealRepositoryBaselineOptions{VerifierExecutable: executable}, prepare, platform.RunProcess, (*PreparedRealRepository).Close)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.wantStatus || result.ActualExitCode != test.actual || result.Mode != "local_unisolated" {
				t.Fatalf("baseline result = %#v, want status %q and exit %d", result, test.wantStatus, test.actual)
			}
			if result.RunID == "" || result.ManifestSHA256 == "" || result.VerifierSHA256 == "" {
				t.Fatalf("baseline evidence missing run or version hashes: %#v", result)
			}
			if _, err := os.Stat(*root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("baseline root left behind: %v", err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private verifier detail") || strings.Contains(string(encoded), manifest.Tasks[0].Prompt) {
				t.Fatalf("result leaked private text: %s", encoded)
			}
		})
	}
}

func TestRealRepositoryBaselineOutputLimit(t *testing.T) {
	manifest, executable := baselineHelperManifest(t, "5s", 0)
	prepare, _ := baselineFakePrepare(t, "large-output")
	result, err := runRealRepositoryBaseline(context.Background(), manifest, manifest.Tasks[0].ID,
		RealRepositoryBaselineOptions{VerifierExecutable: executable}, prepare, platform.RunProcess, (*PreparedRealRepository).Close)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "output_limit" || !result.OutputTruncated || result.StdoutBytes != realBaselineStreamLimit {
		t.Fatalf("output limit result = %#v", result)
	}
	wantHash := sha256.Sum256([]byte(strings.Repeat("x", realBaselineStreamLimit)))
	if result.StdoutSHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("stdout hash = %q, want captured-prefix hash", result.StdoutSHA256)
	}
}

func TestRealRepositoryBaselineTimeoutAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name, timeout, wantStatus string
		cancel                    bool
	}{
		{name: "timeout", timeout: "100ms", wantStatus: "timeout"},
		{name: "cancel", timeout: "5s", wantStatus: "cancelled", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest, executable := baselineHelperManifest(t, test.timeout, 0)
			prepare, root := baselineFakePrepare(t, "sleep")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel {
				time.AfterFunc(200*time.Millisecond, cancel)
			}
			started := time.Now()
			result, err := runRealRepositoryBaseline(ctx, manifest, manifest.Tasks[0].ID,
				RealRepositoryBaselineOptions{VerifierExecutable: executable}, prepare, platform.RunProcess, (*PreparedRealRepository).Close)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.wantStatus || time.Since(started) > 4*time.Second {
				t.Fatalf("baseline result = %#v, duration = %s", result, time.Since(started))
			}
			if _, err := os.Stat(*root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("baseline root left behind: %v", err)
			}
		})
	}
}

func TestRealRepositoryBaselineCancelsChildProcessTree(t *testing.T) {
	manifest, executable := baselineHelperManifest(t, "10s", 0)
	marker := filepath.Join(t.TempDir(), "escaped-child")
	prepare, _ := baselineFakePrepare(t, "child\n"+marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan bool, 1)
	go func() {
		deadline := time.After(5 * time.Second)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := os.Stat(marker + "-started"); err == nil {
					started <- true
					cancel()
					return
				}
			case <-deadline:
				started <- false
				cancel()
				return
			}
		}
	}()
	result, err := runRealRepositoryBaseline(ctx, manifest, manifest.Tasks[0].ID,
		RealRepositoryBaselineOptions{VerifierExecutable: executable}, prepare, platform.RunProcess, (*PreparedRealRepository).Close)
	if err != nil || result.Status != "cancelled" {
		t.Fatalf("child cancellation result = %#v, err = %v", result, err)
	}
	if !<-started {
		t.Fatal("verifier did not start a child before cancellation")
	}
	time.Sleep(1800 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verifier child survived cancellation: %v", err)
	}
}

func TestRealRepositoryBaselinePreflightRejectsWrongBinary(t *testing.T) {
	manifest, executable := baselineHelperManifest(t, "5s", 0)
	prepareCalls := 0
	prepare := func(context.Context, RealRepositoryManifest, string) (*PreparedRealRepository, error) {
		prepareCalls++
		return nil, errors.New("must not prepare")
	}
	for _, option := range []RealRepositoryBaselineOptions{
		{VerifierExecutable: filepath.Base(executable)},
		{VerifierExecutable: executable + "-wrong"},
	} {
		if _, err := runRealRepositoryBaseline(context.Background(), manifest, manifest.Tasks[0].ID, option,
			prepare, platform.RunProcess, (*PreparedRealRepository).Close); err == nil {
			t.Fatalf("invalid verifier binary %q was accepted", option.VerifierExecutable)
		}
	}
	if prepareCalls != 0 {
		t.Fatalf("source preparation ran %d times before verifier preflight", prepareCalls)
	}
	manifest.Tasks[0].RiskBudget.NetworkAccess = "allowlisted"
	manifest.Tasks[0].RiskBudget.AllowedEffects = append(manifest.Tasks[0].RiskBudget.AllowedEffects, "network")
	if _, err := runRealRepositoryBaseline(context.Background(), manifest, manifest.Tasks[0].ID,
		RealRepositoryBaselineOptions{VerifierExecutable: executable}, prepare,
		platform.RunProcess, (*PreparedRealRepository).Close); err == nil || prepareCalls != 0 {
		t.Fatalf("allowlisted network mode should be rejected before source preparation: err=%v calls=%d", err, prepareCalls)
	}
}

func TestRealRepositoryBaselineRejectsWorkspaceBinaryAndCleanupFailure(t *testing.T) {
	manifest := validRealManifest()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "verifier"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(workspace, name)
	if err := os.WriteFile(executable, []byte("not an executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest.Tasks[0].Verifier.Command = []string{name}
	prepare := func(context.Context, RealRepositoryManifest, string) (*PreparedRealRepository, error) {
		return &PreparedRealRepository{Path: workspace, SourceCommit: manifest.Tasks[0].SourceCommit, root: root}, nil
	}
	result, err := runRealRepositoryBaseline(context.Background(), manifest, manifest.Tasks[0].ID,
		RealRepositoryBaselineOptions{VerifierExecutable: executable}, prepare,
		func(context.Context, platform.ProcessSpec) (platform.ProcessResult, error) {
			t.Fatal("workspace binary must never execute")
			return platform.ProcessResult{}, nil
		}, func(*PreparedRealRepository) error { return nil })
	if err != nil || result.Status != "preflight_failed" {
		t.Fatalf("workspace binary result = %#v, err = %v", result, err)
	}

	manifest, executable = baselineHelperManifest(t, "5s", 0)
	prepare, _ = baselineFakePrepare(t, "success")
	result, err = runRealRepositoryBaseline(context.Background(), manifest, manifest.Tasks[0].ID,
		RealRepositoryBaselineOptions{VerifierExecutable: executable}, prepare,
		func(context.Context, platform.ProcessSpec) (platform.ProcessResult, error) {
			return platform.ProcessResult{ExitCode: 0}, nil
		}, func(*PreparedRealRepository) error { return errors.New("cleanup failed") })
	if err != nil || result.Status != "cleanup_failed" {
		t.Fatalf("cleanup failure result = %#v, err = %v", result, err)
	}
}

func TestRealRepositoryBaselineEnvironmentExcludesSecrets(t *testing.T) {
	t.Setenv("MENGDIE_LIVE_API_KEY", "secret-must-not-appear")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7897")
	manifest, executable := baselineHelperManifest(t, "5s", 0)
	prepare, _ := baselineFakePrepare(t, "success")
	result, err := runRealRepositoryBaseline(context.Background(), manifest, manifest.Tasks[0].ID,
		RealRepositoryBaselineOptions{VerifierExecutable: executable}, prepare,
		func(_ context.Context, spec platform.ProcessSpec) (platform.ProcessResult, error) {
			joined := strings.Join(spec.Env, "\n")
			for _, forbidden := range []string{"secret-must-not-appear", "MENGDIE_LIVE_API_KEY", "HTTPS_PROXY", os.Getenv("HOME") + string(filepath.Separator)} {
				if forbidden != string(filepath.Separator) && strings.Contains(joined, forbidden) {
					t.Errorf("verifier environment contains %q", forbidden)
				}
			}
			if !strings.Contains(joined, "GOPROXY=off") || !strings.Contains(joined, "GOWORK=off") {
				t.Errorf("verifier environment missing offline Go settings: %s", joined)
			}
			return platform.ProcessResult{ExitCode: 0}, nil
		}, (*PreparedRealRepository).Close)
	if err != nil || result.Status != "baseline_matched" {
		t.Fatalf("baseline result = %#v, err = %v", result, err)
	}
}
