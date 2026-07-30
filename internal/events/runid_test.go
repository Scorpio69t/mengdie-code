// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"strings"
	"testing"
)

func TestNewRunID(t *testing.T) {
	first, err := NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "run_") || len(first) != 36 {
		t.Fatalf("NewRunID() = %q", first)
	}
	if first == second {
		t.Fatal("NewRunID() returned a duplicate")
	}
}
