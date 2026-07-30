package pathutil

import "testing"

func TestBaseNameRemovesCompoundExtension(t *testing.T) {
	if got := BaseName("reports/archive.tar.gz"); got != "archive" {
		t.Fatalf("BaseName() = %q, want %q", got, "archive")
	}
}

func TestBaseNameWithoutExtension(t *testing.T) {
	if got := BaseName("README"); got != "README" {
		t.Fatalf("BaseName() = %q, want %q", got, "README")
	}
}