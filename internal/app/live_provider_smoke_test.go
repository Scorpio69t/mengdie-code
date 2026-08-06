//go:build liveprovider

// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/events"
)

func TestLiveProviderCompletesReadOnlyToolTask(t *testing.T) {
	if os.Getenv("MENGDIE_LIVE_SMOKE") != "1" {
		t.Skip("set MENGDIE_LIVE_SMOKE=1 and live Provider variables to run")
	}
	baseURL := requiredLiveEnvironment(t, "MENGDIE_LIVE_BASE_URL")
	apiKey := requiredLiveEnvironment(t, "MENGDIE_LIVE_API_KEY")
	model := requiredLiveEnvironment(t, "MENGDIE_LIVE_MODEL")
	t.Setenv("MENGDIE_PROFILE", "default")
	t.Setenv("MENGDIE_BASE_URL", baseURL)
	t.Setenv("MENGDIE_MODEL", model)
	t.Setenv("MENGDIE_API_KEY_ENV", "MENGDIE_LIVE_API_KEY")

	root := t.TempDir()
	const fixtureName = "MENGDIE_SMOKE.txt"
	const fixtureContent = "mengdie-live-readonly-ok\n"
	fixturePath := filepath.Join(root, fixtureName)
	if err := os.WriteFile(fixturePath, []byte(fixtureContent), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, ".mengdie", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	configContent := fmt.Sprintf(`
[profiles.default]
provider = "openai-compatible"
base_url = %s
api_key_env = "MENGDIE_LIVE_API_KEY"
model = %s
request_timeout = "120s"
max_context_tokens = 64000
`, strconv.Quote(baseURL), strconv.Quote(model))
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	application := New(BuildInfo{Version: "live-smoke", Commit: "manual"}, &stdout, &stderr)
	application.userConfigDir = t.TempDir()
	code := application.Run(context.Background(), []string{
		"exec", "--cwd", root, "--json", "--max-turns", "8",
		"必须使用 read_file 读取 MENGDIE_SMOKE.txt，然后只回答文件中的标记；禁止修改文件或执行命令。",
	}, false)
	if strings.Contains(stdout.String(), apiKey) || strings.Contains(stderr.String(), apiKey) {
		t.Fatal("live smoke output leaked the Provider credential")
	}
	if code != ExitOK {
		t.Fatalf("live smoke exit=%d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	content, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != fixtureContent {
		t.Fatalf("live smoke modified fixture: %q", content)
	}

	sawRead := false
	sawCompleted := false
	for index, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var event events.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event %d: %v", index, err)
		}
		switch event.Kind {
		case events.KindToolProposed:
			var proposed events.ToolProposed
			if err := json.Unmarshal(event.Payload, &proposed); err != nil {
				t.Fatal(err)
			}
			sawRead = sawRead || proposed.Tool == "read_file"
		case events.KindRunCompleted:
			sawCompleted = true
		}
	}
	if !sawRead || !sawCompleted {
		t.Fatalf("live smoke did not prove the read-only loop: read=%t completed=%t", sawRead, sawCompleted)
	}
}

func requiredLiveEnvironment(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required when MENGDIE_LIVE_SMOKE=1", name)
	}
	return value
}
