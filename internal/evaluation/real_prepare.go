// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package evaluation

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	realRepoPrepareTimeout = 5 * time.Minute
	realRepoGitOutputLimit = 16 * 1024
)

// GitInvocation describes one controlled Git subprocess. Args are passed
// directly to exec.CommandContext; no shell is involved.
type GitInvocation struct {
	Directory   string
	Environment []string
	Args        []string
}

// GitExecutor makes source preparation testable without contacting a network.
type GitExecutor interface {
	Run(context.Context, GitInvocation) (GitOutput, error)
}

// GitOutput contains bounded stdout/stderr from a Git subprocess.
type GitOutput struct {
	Stdout string
	Stderr string
}

type systemGitExecutor struct{}

func (systemGitExecutor) Run(ctx context.Context, invocation GitInvocation) (GitOutput, error) {
	command := exec.CommandContext(ctx, "git", invocation.Args...)
	command.Dir = invocation.Directory
	command.Env = invocation.Environment
	stdout := newLimitWriter(realRepoGitOutputLimit)
	stderr := newLimitWriter(realRepoGitOutputLimit)
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	return GitOutput{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

// PreparedRealRepository is a temporary, detached checkout of one task's
// pinned source revision. Close removes the whole temporary root.
type PreparedRealRepository struct {
	Path         string
	SourceCommit string
	root         string
	closeOnce    sync.Once
	closeErr     error
}

// Close removes the temporary checkout and its Git support files.
func (prepared *PreparedRealRepository) Close() error {
	if prepared == nil || prepared.root == "" {
		return nil
	}
	prepared.closeOnce.Do(func() {
		prepared.closeErr = os.RemoveAll(prepared.root)
	})
	return prepared.closeErr
}

// PrepareRealRepositoryTask fetches a fixed public GitHub commit into a private
// temporary checkout. It never runs project code, a verifier, or an Agent.
func PrepareRealRepositoryTask(ctx context.Context, manifest RealRepositoryManifest, taskID string) (*PreparedRealRepository, error) {
	return prepareRealRepositoryTask(ctx, manifest, taskID, systemGitExecutor{})
}

func prepareRealRepositoryTask(ctx context.Context, manifest RealRepositoryManifest, taskID string, git GitExecutor) (_ *PreparedRealRepository, err error) {
	if ctx == nil {
		return nil, errors.New("real repository prepare requires a context")
	}
	if git == nil {
		return nil, errors.New("real repository prepare requires a Git executor")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("real repository prepare canceled: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid real repository manifest: %w", err)
	}
	task, ok := manifestTaskByID(manifest, taskID)
	if !ok {
		return nil, fmt.Errorf("real repository task %q not found", taskID)
	}
	if err := validateFetchHost(task.SourceURL); err != nil {
		return nil, fmt.Errorf("task %q source is not allowed: %w", task.ID, err)
	}

	root, err := os.MkdirTemp("", "mengdie-real-repo-*")
	if err != nil {
		return nil, fmt.Errorf("create private real repository workspace: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			if cleanupErr := os.RemoveAll(root); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("remove failed real repository workspace: %w", cleanupErr))
			}
		}
	}()
	if err := secureRealRepositoryRoot(root); err != nil {
		return nil, fmt.Errorf("secure real repository workspace: %w", err)
	}

	workspace := filepath.Join(root, "workspace")
	hooksDir := filepath.Join(root, "empty-hooks")
	templateDir := filepath.Join(root, "empty-template")
	globalConfig := filepath.Join(root, "empty.gitconfig")
	for _, directory := range []string{workspace, hooksDir, templateDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return nil, fmt.Errorf("prepare private Git support directory: %w", err)
		}
	}
	if err := os.WriteFile(globalConfig, nil, 0o600); err != nil {
		return nil, fmt.Errorf("prepare isolated Git configuration: %w", err)
	}

	prepareContext, cancel := context.WithTimeout(ctx, realRepoPrepareTimeout)
	defer cancel()
	commandConfig := []string{
		"-c", "credential.helper=",
		"-c", "http.followRedirects=false",
		"-c", "core.hooksPath=" + hooksDir,
		"-c", "protocol.file.allow=never",
		"-c", "protocol.ext.allow=never",
		"-c", "filter.lfs.smudge=",
		"-c", "filter.lfs.required=false",
		"-c", "submodule.recurse=false",
		"-c", "fetch.fsckObjects=true",
		"-c", "transfer.fsckObjects=true",
	}
	environment := isolatedGitEnvironment(globalConfig)
	run := func(stage string, args ...string) (GitOutput, error) {
		if err := prepareContext.Err(); err != nil {
			return GitOutput{}, fmt.Errorf("real repository prepare canceled during %s: %w", stage, err)
		}
		allArgs := append(append([]string(nil), commandConfig...), args...)
		output, runErr := git.Run(prepareContext, GitInvocation{Directory: workspace, Environment: environment, Args: allArgs})
		if runErr != nil {
			return GitOutput{}, fmt.Errorf("Git %s failed", stage)
		}
		return output, nil
	}

	if _, err := run("init", "-c", "init.templateDir="+templateDir, "init", "--quiet", "."); err != nil {
		return nil, err
	}
	if _, err := run("fetch", "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--depth=1", task.SourceURL, task.SourceCommit); err != nil {
		return nil, err
	}
	if _, err := run("checkout", "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return nil, err
	}
	actualCommit, err := run("commit verification", "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(actualCommit.Stdout) != task.SourceCommit {
		return nil, errors.New("Git checkout did not match the pinned source commit")
	}
	status, err := run("clean checkout verification", "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(status.Stdout) != "" {
		return nil, errors.New("Git checkout is not clean after preparation")
	}
	if err := prepareContext.Err(); err != nil {
		return nil, fmt.Errorf("real repository prepare canceled: %w", err)
	}

	cleanup = false
	return &PreparedRealRepository{Path: workspace, SourceCommit: task.SourceCommit, root: root}, nil
}

func manifestTaskByID(manifest RealRepositoryManifest, taskID string) (RealRepositoryTask, bool) {
	for _, task := range manifest.Tasks {
		if task.ID == taskID {
			return task, true
		}
	}
	return RealRepositoryTask{}, false
}

func validateFetchHost(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return errors.New("source host must be github.com over HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("source URL must not include credentials, query parameters, or fragments")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return errors.New("source port must be 443")
	}
	return nil
}

func isolatedGitEnvironment(globalConfig string) []string {
	allowed := map[string]bool{
		"ALL_PROXY": true, "HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true,
		"PATH": true, "HOME": true, "USERPROFILE": true, "SYSTEMROOT": true, "WINDIR": true,
		"TMP": true, "TEMP": true, "TMPDIR": true, "LANG": true, "LC_ALL": true, "TZ": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	}
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !allowed[strings.ToUpper(key)] {
			continue
		}
		values[key] = value
	}
	setEnvironment(values, "GIT_CONFIG_NOSYSTEM", "1")
	setEnvironment(values, "GIT_CONFIG_GLOBAL", globalConfig)
	setEnvironment(values, "GIT_TERMINAL_PROMPT", "0")
	setEnvironment(values, "GIT_ALLOW_PROTOCOL", "https")
	setEnvironment(values, "GIT_LFS_SKIP_SMUDGE", "1")
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

func setEnvironment(values map[string]string, key, value string) {
	for existing := range values {
		if strings.EqualFold(existing, key) {
			delete(values, existing)
		}
	}
	values[key] = value
}
