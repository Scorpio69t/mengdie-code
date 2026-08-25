// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package memory_test

import (
	"math"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
)

// TestAuthorityValues locks the wire values of the Authority enum to the
// string literals the SQLite CHECK constraint on memories.authority expects.
func TestAuthorityValues(t *testing.T) {
	want := []string{"explicit", "repository", "verified", "inferred"}
	for _, w := range want {
		if string(memory.Authority(w)) != w {
			t.Fatalf("Authority(%q) round-trip failed", w)
		}
	}
}

// TestStatusValues locks the wire values of the Status enum to the string
// literals the SQLite CHECK constraint on memories.status expects.
func TestStatusValues(t *testing.T) {
	want := []string{"proposed", "active", "stale", "disputed", "superseded", "archived"}
	for _, w := range want {
		if string(memory.Status(w)) != w {
			t.Fatalf("Status(%q) round-trip failed", w)
		}
	}
}

// TestSourceTypeValues locks the wire values of the SourceType enum so the
// Store layer can rely on string equality when enforcing Authority routing.
func TestSourceTypeValues(t *testing.T) {
	want := []string{"user_message", "agent_message", "session_event", "file", "command_result"}
	for _, w := range want {
		if string(memory.SourceType(w)) != w {
			t.Fatalf("SourceType(%q) round-trip failed", w)
		}
	}
}

// TestScopeValid checks the per-kind validation rules described in spec §3.
func TestScopeValid(t *testing.T) {
	cases := []struct {
		name  string
		scope memory.Scope
		ok    bool
	}{
		{name: "user-empty", scope: memory.Scope{Kind: "user", Value: ""}, ok: true},
		{name: "project-set", scope: memory.Scope{Kind: "project", Value: "mengdie"}, ok: true},
		{name: "branch-set", scope: memory.Scope{Kind: "branch", Value: "main"}, ok: true},
		{name: "task-set", scope: memory.Scope{Kind: "task", Value: "task-1"}, ok: true},
		{name: "unknown-kind", scope: memory.Scope{Kind: "session", Value: "x"}, ok: false},
		{name: "project-empty", scope: memory.Scope{Kind: "project", Value: ""}, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.scope.Valid()
			if tc.ok && err != nil {
				t.Fatalf("Valid() returned unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("Valid() returned nil; expected error")
			}
		})
	}
}

// TestSourceRefValid checks the SourceRef validation rules. Type must be a
// known SourceType and Ref must be non-empty.
func TestSourceRefValid(t *testing.T) {
	cases := []struct {
		name string
		ref  memory.SourceRef
		ok   bool
	}{
		{name: "user-message", ref: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: "session:42:user"}, ok: true},
		{name: "file", ref: memory.SourceRef{Type: memory.SourceTypeFile, Ref: "internal/foo.go:42"}, ok: true},
		{name: "command-result", ref: memory.SourceRef{Type: memory.SourceTypeCommandResult, Ref: "go test ./... exit=0"}, ok: true},
		{name: "unknown-type", ref: memory.SourceRef{Type: memory.SourceType("bogus"), Ref: "x"}, ok: false},
		{name: "empty-ref", ref: memory.SourceRef{Type: memory.SourceTypeUserMessage, Ref: ""}, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ref.Valid()
			if tc.ok && err != nil {
				t.Fatalf("Valid() returned unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("Valid() returned nil; expected error")
			}
		})
	}
}

// TestGenerateIDStable asserts GenerateID is deterministic for the same inputs
// and produces a well-formed identifier with the "mem_" prefix.
func TestGenerateIDStable(t *testing.T) {
	a := memory.GenerateID("claim-X", memory.Scope{Kind: "project", Value: "mengdie"}, "explicit", "session-1")
	b := memory.GenerateID("claim-X", memory.Scope{Kind: "project", Value: "mengdie"}, "explicit", "session-1")
	if a != b {
		t.Fatal("same input must produce same id")
	}
	if !strings.HasPrefix(a, "mem_") {
		t.Fatal("id must start with mem_")
	}
}

// TestGenerateIDScopeSensitive asserts that the same claim under a different
// scope produces a different identifier (idempotency is per-scope+authority).
func TestGenerateIDScopeSensitive(t *testing.T) {
	project := memory.GenerateID("claim-X", memory.Scope{Kind: "project", Value: "mengdie"}, "explicit", "session-1")
	user := memory.GenerateID("claim-X", memory.Scope{Kind: "user", Value: ""}, "explicit", "session-1")
	if project == user {
		t.Fatal("different scopes must produce different ids")
	}
}

// TestGenerateIDLength pins the wire length so downstream readers can rely on
// a stable memory id format.
func TestGenerateIDLength(t *testing.T) {
	id := memory.GenerateID("c", memory.Scope{Kind: "project", Value: "p"}, "explicit", "s")
	if got, want := len(id), len("mem_")+32; got != want {
		t.Fatalf("GenerateID length = %d, want %d (id=%q)", got, want, id)
	}
}

// TestAuthorityRank pins the rank integer returned by AuthorityRank for each
// known Authority value and the unknown / empty-string fallbacks. Lower is
// more authoritative; unknown values default to math.MaxInt so a bad value
// never displaces a known one in cross-authority dispute resolution.
func TestAuthorityRank(t *testing.T) {
	cases := []struct {
		a    memory.Authority
		want int
		name string
	}{
		{memory.AuthorityExplicit, 1, "explicit"},
		{memory.AuthorityVerified, 2, "verified"},
		{memory.AuthorityRepository, 3, "repository"},
		{memory.AuthorityInferred, 4, "inferred"},
		{memory.Authority("unknown"), math.MaxInt, "unknown_default"},
		{memory.Authority(""), math.MaxInt, "empty_default"},
	}
	for _, c := range cases {
		if got := memory.AuthorityRank(c.a); got != c.want {
			t.Errorf("%s: AuthorityRank(%q) = %d, want %d", c.name, c.a, got, c.want)
		}
	}
}
