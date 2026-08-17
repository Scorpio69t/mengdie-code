// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type rewindBackendStub struct {
	target    RewindTarget
	started   int
	completed int
}

func (backend *rewindBackendStub) InspectRewind(context.Context, string, string, string) (RewindTarget, error) {
	target := backend.target
	target.PreContent = append([]byte(nil), target.PreContent...)
	target.PostContent = append([]byte(nil), target.PostContent...)
	return target, nil
}

func (backend *rewindBackendStub) StartRewind(context.Context, string, string, string) error {
	backend.started++
	return nil
}

func (backend *rewindBackendStub) CompleteRewind(context.Context, string, string, string) error {
	backend.completed++
	return nil
}

func TestRewindFileRestoresAndDeletesOnlyAfterCapability(t *testing.T) {
	tests := []struct {
		name      string
		preExists bool
		before    string
		operation string
	}{
		{name: "restore existing file", preExists: true, before: "before\n", operation: "restored"},
		{name: "delete created file", preExists: false, operation: "deleted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newToolTestEnv(t)
			path := filepath.Join(env.root, "value.txt")
			env.write(t, "value.txt", "after\n")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			backend := &rewindBackendStub{target: RewindTarget{
				SessionID: "session-1", JournalID: "journal-1", Path: path,
				PathFingerprint: bytesSHA256([]byte(path)), PreExists: test.preExists,
				PreContent: []byte(test.before), PreSHA256: bytesSHA256([]byte(test.before)), PreMode: info.Mode().Perm(),
				PostContent: []byte("after\n"), PostSHA256: bytesSHA256([]byte("after\n")), PostMode: info.Mode().Perm(),
			}}
			if !test.preExists {
				backend.target.PreSHA256 = ""
			}
			tool, err := NewRewindFile(backend, "command-1")
			if err != nil {
				t.Fatal(err)
			}
			call, err := tool.Prepare(context.Background(), json.RawMessage(`{"session_id":"session-1","journal_id":"journal-1"}`), env.prepareEnv())
			if err != nil {
				t.Fatal(err)
			}
			result, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv())
			if err != nil {
				t.Fatal(err)
			}
			if result.Metadata["operation"] != test.operation || backend.started != 1 || backend.completed != 1 {
				t.Fatalf("result=%+v started=%d completed=%d", result, backend.started, backend.completed)
			}
			content, readErr := os.ReadFile(path)
			if test.preExists {
				if readErr != nil || string(content) != test.before {
					t.Fatalf("content=%q err=%v", content, readErr)
				}
			} else if !os.IsNotExist(readErr) {
				t.Fatalf("created file still exists: %v", readErr)
			}
		})
	}
}

func TestRewindFileRejectsPostApprovalContentOrModeChange(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "content", mutate: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("user edit\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "mode", mutate: func(t *testing.T, path string) {
			if runtime.GOOS == "windows" {
				t.Skip("Windows does not expose portable permission-bit changes")
			}
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newToolTestEnv(t)
			path := filepath.Join(env.root, "value.txt")
			env.write(t, "value.txt", "after\n")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			backend := &rewindBackendStub{target: RewindTarget{
				SessionID: "session-1", JournalID: "journal-1", Path: path,
				PathFingerprint: bytesSHA256([]byte(path)), PreExists: true,
				PreContent: []byte("before\n"), PreSHA256: bytesSHA256([]byte("before\n")), PreMode: info.Mode().Perm(),
				PostContent: []byte("after\n"), PostSHA256: bytesSHA256([]byte("after\n")), PostMode: info.Mode().Perm(),
			}}
			tool, _ := NewRewindFile(backend, "command-1")
			call, err := tool.Prepare(context.Background(), json.RawMessage(`{"session_id":"session-1","journal_id":"journal-1"}`), env.prepareEnv())
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, path)
			if _, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv()); err == nil {
				t.Fatal("Execute() accepted a changed post-state")
			}
			if backend.started != 0 || backend.completed != 0 {
				t.Fatalf("started=%d completed=%d", backend.started, backend.completed)
			}
		})
	}
}
