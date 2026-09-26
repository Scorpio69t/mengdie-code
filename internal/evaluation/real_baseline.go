// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package evaluation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/platform"
)

const (
	realBaselineStreamLimit     = 32 << 10
	realBaselineExecutableLimit = 128 << 20
)

// RealRepositoryBaselineOptions explicitly selects the operator-approved
// verifier binary. Local diagnostics execute untrusted repository code without
// an OS sandbox; callers must opt in at their user-facing boundary.
type RealRepositoryBaselineOptions struct {
	VerifierExecutable string
}

// RealRepositoryBaselineResult contains only bounded, redacted evidence. A
// matched baseline is not an Agent success or an M1 acceptance result.
type RealRepositoryBaselineResult struct {
	SchemaVersion          int       `json:"schema_version"`
	RunID                  string    `json:"run_id"`
	Mode                   string    `json:"mode"`
	ManifestID             string    `json:"manifest_id"`
	ManifestSHA256         string    `json:"manifest_sha256"`
	TaskID                 string    `json:"task_id"`
	SourceCommit           string    `json:"source_commit"`
	VerifierSHA256         string    `json:"verifier_sha256"`
	Platform               string    `json:"platform"`
	StartedAt              time.Time `json:"started_at"`
	DurationMillis         int64     `json:"duration_ms"`
	Status                 string    `json:"status"`
	ExpectedExitCode       int       `json:"expected_exit_code"`
	ActualExitCode         int       `json:"actual_exit_code"`
	VerifierDurationMillis int64     `json:"verifier_duration_ms"`
	StdoutSHA256           string    `json:"stdout_sha256"`
	StderrSHA256           string    `json:"stderr_sha256"`
	StdoutBytes            int64     `json:"stdout_bytes"`
	StderrBytes            int64     `json:"stderr_bytes"`
	OutputTruncated        bool      `json:"output_truncated"`
	ForcedCleanup          bool      `json:"forced_cleanup"`
}

type realBaselinePrepare func(context.Context, RealRepositoryManifest, string) (*PreparedRealRepository, error)
type realBaselineProcess func(context.Context, platform.ProcessSpec) (platform.ProcessResult, error)
type realBaselineClose func(*PreparedRealRepository) error

// RunRealRepositoryBaseline runs one public verifier against a fresh pinned
// checkout. It does not run an Agent or establish filesystem/network isolation.
func RunRealRepositoryBaseline(ctx context.Context, manifest RealRepositoryManifest, taskID string, options RealRepositoryBaselineOptions) (RealRepositoryBaselineResult, error) {
	return runRealRepositoryBaseline(ctx, manifest, taskID, options, PrepareRealRepositoryTask, platform.RunProcess, (*PreparedRealRepository).Close)
}

func runRealRepositoryBaseline(ctx context.Context, manifest RealRepositoryManifest, taskID string, options RealRepositoryBaselineOptions, prepare realBaselinePrepare, run realBaselineProcess, closeWorkspace realBaselineClose) (result RealRepositoryBaselineResult, err error) {
	if ctx == nil || prepare == nil || run == nil || closeWorkspace == nil {
		return result, errors.New("real repository baseline requires context and execution dependencies")
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("real repository baseline canceled: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return result, fmt.Errorf("invalid real repository manifest: %w", err)
	}
	task, ok := manifestTaskByID(manifest, taskID)
	if !ok {
		return result, fmt.Errorf("real repository task %q not found", taskID)
	}
	if task.RiskBudget.NetworkAccess == "allowlisted" {
		return result, errors.New("allowlisted verifier network is unsupported by local baseline diagnostics")
	}
	executable, err := resolveRealBaselineExecutable(options.VerifierExecutable, task.Verifier.Command[0])
	if err != nil {
		return result, fmt.Errorf("invalid verifier executable: %w", err)
	}
	timeout, err := task.Verifier.duration()
	if err != nil {
		return result, fmt.Errorf("invalid verifier timeout: %w", err)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return result, errors.New("cannot encode validated real repository manifest")
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	verifierDigest, err := hashRealBaselineExecutable(executable)
	if err != nil {
		return result, err
	}
	var runID [16]byte
	if _, err := rand.Read(runID[:]); err != nil {
		return result, errors.New("cannot create baseline run id")
	}

	started := time.Now().UTC()
	result = RealRepositoryBaselineResult{
		SchemaVersion:    1,
		RunID:            hex.EncodeToString(runID[:]),
		Mode:             "local_unisolated",
		ManifestID:       manifest.ID,
		ManifestSHA256:   hex.EncodeToString(manifestDigest[:]),
		TaskID:           task.ID,
		SourceCommit:     task.SourceCommit,
		VerifierSHA256:   verifierDigest,
		Platform:         runtime.GOOS + "/" + runtime.GOARCH,
		StartedAt:        started,
		Status:           "source_prepare_failed",
		ExpectedExitCode: task.Baseline.ExpectedExitCode,
		ActualExitCode:   -1,
	}
	defer func() { result.DurationMillis = time.Since(started).Milliseconds() }()

	prepared, prepareErr := prepare(ctx, manifest, taskID)
	if prepareErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			result.Status = "cancelled"
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Status = "timeout"
		}
		return result, nil
	}
	if prepared != nil {
		defer func() {
			if cleanupErr := closeWorkspace(prepared); cleanupErr != nil {
				result.Status = "cleanup_failed"
			}
		}()
	}
	if prepared == nil || prepared.Path == "" || prepared.SourceCommit != task.SourceCommit {
		result.Status = "source_prepare_failed"
		return result, nil
	}
	if executableInsideWorkspace(executable, prepared.Path) || executableInsideWorkspace(options.VerifierExecutable, prepared.Path) {
		result.Status = "preflight_failed"
		return result, nil
	}

	stateRoot, stateErr := os.MkdirTemp(filepath.Dir(prepared.Path), "verifier-state-")
	if stateErr != nil {
		result.Status = "preflight_failed"
		return result, nil
	}
	for _, name := range []string{"home", "tmp", "cache", "gopath"} {
		if err := os.Mkdir(filepath.Join(stateRoot, name), 0o700); err != nil {
			result.Status = "preflight_failed"
			return result, nil
		}
	}
	environment := realBaselineEnvironment(executable, stateRoot)
	verificationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout := newRealBaselineOutput(cancel)
	stderr := newRealBaselineOutput(cancel)
	processResult, processErr := run(verificationCtx, platform.ProcessSpec{
		Executable: executable,
		Args:       append([]string(nil), task.Verifier.Command[1:]...),
		Dir:        prepared.Path,
		Env:        environment,
		Stdout:     stdout,
		Stderr:     stderr,
		KillGrace:  2 * time.Second,
	})
	result.ActualExitCode = processResult.ExitCode
	result.VerifierDurationMillis = processResult.Duration.Milliseconds()
	result.ForcedCleanup = processResult.ForcedCleanup
	var stdoutTruncated, stderrTruncated bool
	result.StdoutSHA256, result.StdoutBytes, stdoutTruncated = stdout.snapshot()
	result.StderrSHA256, result.StderrBytes, stderrTruncated = stderr.snapshot()
	result.OutputTruncated = stdoutTruncated || stderrTruncated
	switch {
	case result.OutputTruncated:
		result.Status = "output_limit"
	case errors.Is(ctx.Err(), context.Canceled):
		result.Status = "cancelled"
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(verificationCtx.Err(), context.DeadlineExceeded):
		result.Status = "timeout"
	case processErr != nil:
		result.Status = "execution_error"
	case result.ForcedCleanup:
		result.Status = "indeterminate"
	case result.ActualExitCode != result.ExpectedExitCode:
		result.Status = "baseline_mismatch"
	default:
		result.Status = "baseline_matched"
	}
	return result, nil
}

func resolveRealBaselineExecutable(provided, commandName string) (string, error) {
	if !filepath.IsAbs(provided) {
		return "", errors.New("--verifier-bin must be an absolute path")
	}
	if filepath.Base(commandName) != commandName || strings.ContainsAny(commandName, `/\`) {
		return "", errors.New("verifier command must begin with a bare executable name")
	}
	resolved, err := filepath.EvalSymlinks(provided)
	if err != nil {
		return "", errors.New("cannot resolve verifier binary")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("verifier binary must be a regular file")
	}
	name := filepath.Base(resolved)
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(strings.ToLower(name), ".exe")
		commandName = strings.TrimSuffix(strings.ToLower(commandName), ".exe")
	}
	if name != commandName {
		return "", errors.New("verifier binary does not match manifest command")
	}
	return resolved, nil
}

func hashRealBaselineExecutable(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open verifier binary")
	}
	defer file.Close()
	digest := sha256.New()
	count, err := io.CopyN(digest, file, realBaselineExecutableLimit+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("cannot hash verifier binary")
	}
	if count > realBaselineExecutableLimit {
		return "", errors.New("verifier binary exceeds size limit")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func executableInsideWorkspace(executable, workspace string) bool {
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return true
	}
	relative, err := filepath.Rel(canonicalWorkspace, executable)
	if err != nil {
		return true
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func realBaselineEnvironment(executable, stateRoot string) []string {
	home := filepath.Join(stateRoot, "home")
	tmp := filepath.Join(stateRoot, "tmp")
	cache := filepath.Join(stateRoot, "cache")
	gopath := filepath.Join(stateRoot, "gopath")
	environment := []string{
		"PATH=" + filepath.Dir(executable), "HOME=" + home, "USERPROFILE=" + home,
		"TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp,
		"GOCACHE=" + cache, "GOPATH=" + gopath, "GOMODCACHE=" + filepath.Join(gopath, "pkg", "mod"),
		"GOWORK=off", "GOENV=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0",
		"CI=1", "NO_COLOR=1", "GIT_TERMINAL_PROMPT=0",
	}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SYSTEMROOT", "WINDIR"} {
			if value := os.Getenv(key); value != "" {
				environment = append(environment, key+"="+value)
			}
		}
	}
	return environment
}

// RealAgentEnvironment gives Shell a private home/cache and no inherited
// credential or proxy variables. It does not block direct network access.
func RealAgentEnvironment(verifierExecutable, stateRoot string) ([]string, error) {
	for _, name := range []string{"home", "tmp", "cache", "gopath"} {
		if err := os.MkdirAll(filepath.Join(stateRoot, name), 0o700); err != nil {
			return nil, errors.New("cannot prepare agent environment")
		}
	}
	environment := realBaselineEnvironment(verifierExecutable, stateRoot)
	for index, entry := range environment {
		if strings.HasPrefix(entry, "PATH=") {
			path := filepath.Dir(verifierExecutable)
			if runtime.GOOS == "windows" {
				if systemRoot := os.Getenv("SYSTEMROOT"); systemRoot != "" {
					path += string(os.PathListSeparator) + filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0")
				}
			} else {
				path += string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin"
			}
			environment[index] = "PATH=" + path
			break
		}
	}
	return environment, nil
}

type realBaselineOutput struct {
	mu        sync.Mutex
	hash      hash.Hash
	bytes     int64
	truncated bool
	cancel    context.CancelFunc
}

func newRealBaselineOutput(cancel context.CancelFunc) *realBaselineOutput {
	return &realBaselineOutput{hash: sha256.New(), cancel: cancel}
}

func (output *realBaselineOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	remaining := int64(realBaselineStreamLimit) - output.bytes
	count := len(data)
	if remaining < int64(count) {
		count = int(remaining)
		output.truncated = true
	}
	if count > 0 {
		_, _ = output.hash.Write(data[:count])
		output.bytes += int64(count)
	}
	truncated := output.truncated
	output.mu.Unlock()
	if truncated {
		output.cancel()
	}
	return len(data), nil
}

func (output *realBaselineOutput) snapshot() (string, int64, bool) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return hex.EncodeToString(output.hash.Sum(nil)), output.bytes, output.truncated
}
