package name

import "testing"

func TestNormalizeName(t *testing.T) {
	if got := NormalizeName("  MengDie  "); got != "mengdie" {
		t.Fatalf("NormalizeName() = %q, want %q", got, "mengdie")
	}
}
