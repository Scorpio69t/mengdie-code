// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadedProjectIdentityValueFallbackToBaseName(t *testing.T) {
	loaded := Loaded{ProjectRoot: filepath.Join(string(os.PathSeparator), "tmp", "mengdie-code")}
	if got, want := loaded.ProjectIdentityValue(), "mengdie-code"; got != want {
		t.Fatalf("ProjectIdentityValue() = %q, want %q", got, want)
	}
}

func TestLoadedProjectIdentityValueExplicitOverrides(t *testing.T) {
	loaded := Loaded{ProjectRoot: filepath.Join(string(os.PathSeparator), "tmp", "mengdie-code"), ProjectIdentity: "explicit-id"}
	if got, want := loaded.ProjectIdentityValue(), "explicit-id"; got != want {
		t.Fatalf("ProjectIdentityValue() = %q, want %q (explicit must override)", got, want)
	}
}

func TestLoadedProjectIdentityValueEmptyRootEmpty(t *testing.T) {
	loaded := Loaded{ProjectRoot: ""}
	if got := loaded.ProjectIdentityValue(); got != "" {
		t.Fatalf("ProjectIdentityValue() with empty ProjectRoot = %q, want empty", got)
	}
}
