// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
)

const (
	maxShellEnvironment   = 256
	maxAllowedEnvironment = 32
)

// ErrShellEnvironmentChanged prevents an approval from being reused after
// inherited environment names or values change.
var ErrShellEnvironmentChanged = errors.New("shell environment changed after approval")

type environmentBinding struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

func environmentOrCurrent(environment []string) []string {
	if environment == nil {
		return os.Environ()
	}
	return append([]string(nil), environment...)
}

func buildApprovedEnvironment(base, allowedNames []string) ([]string, []environmentBinding, []string, error) {
	if len(allowedNames) > maxAllowedEnvironment {
		return nil, nil, nil, fmt.Errorf("shell: at most %d environment variables may be explicitly allowed", maxAllowedEnvironment)
	}
	allowed := make(map[string]struct{}, len(allowedNames))
	normalizedAllowed := make([]string, 0, len(allowedNames))
	for _, name := range allowedNames {
		if !validEnvironmentName(name) {
			return nil, nil, nil, fmt.Errorf("shell: invalid allowed environment name %q", name)
		}
		key := environmentKey(name)
		if _, exists := allowed[key]; exists {
			continue
		}
		allowed[key] = struct{}{}
		normalizedAllowed = append(normalizedAllowed, name)
	}
	sort.Slice(normalizedAllowed, func(i, j int) bool {
		return environmentKey(normalizedAllowed[i]) < environmentKey(normalizedAllowed[j])
	})

	values := make(map[string]string)
	names := make(map[string]string)
	for _, entry := range base {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !validEnvironmentName(name) {
			continue
		}
		key := environmentKey(name)
		if suspiciousEnvironmentName(name) {
			if _, approved := allowed[key]; !approved {
				continue
			}
		}
		names[key] = name
		values[key] = value
	}
	for name, value := range map[string]string{
		"CI": "1", "GIT_TERMINAL_PROMPT": "0", "GIT_PAGER": "cat", "PAGER": "cat", "NO_COLOR": "1", "TERM": "dumb",
	} {
		key := environmentKey(name)
		names[key] = name
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > maxShellEnvironment {
		return nil, nil, nil, fmt.Errorf("shell: inherited environment has %d variables, limit is %d", len(keys), maxShellEnvironment)
	}
	environment := make([]string, 0, len(keys))
	bindings := make([]environmentBinding, 0, len(keys))
	for _, key := range keys {
		name, value := names[key], values[key]
		environment = append(environment, name+"="+value)
		bindings = append(bindings, environmentBinding{Name: name, SHA256: bytesSHA256([]byte(value))})
	}
	return environment, bindings, normalizedAllowed, nil
}

func validEnvironmentName(name string) bool {
	if name == "" || !isEnvironmentNameStart(name[0]) {
		return false
	}
	for index := 1; index < len(name); index++ {
		char := name[index]
		if !isEnvironmentNameStart(char) && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func isEnvironmentNameStart(char byte) bool {
	return char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}

func environmentKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

func suspiciousEnvironmentName(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "ACCESS_KEY", "PRIVATE_KEY", "CREDENTIAL", "COOKIE", "SESSION"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	for _, prefix := range []string{"AWS_", "AZURE_", "GOOGLE_", "GITHUB_", "GH_", "OPENAI_", "ANTHROPIC_", "DEEPSEEK_", "MOONSHOT_", "ZHIPUAI_", "SSH_", "GPG_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return slices.Contains([]string{"KUBECONFIG", "DOCKER_CONFIG", "NETRC", "NPM_CONFIG_USERCONFIG"}, upper)
}
