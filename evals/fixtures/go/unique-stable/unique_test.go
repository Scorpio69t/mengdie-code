package unique

import (
	"slices"
	"testing"
)

func TestUniquePreservesFirstOccurrence(t *testing.T) {
	want := []string{"dream", "code", "memory"}
	got := Unique([]string{"dream", "code", "dream", "memory", "code"})
	if !slices.Equal(got, want) {
		t.Fatalf("Unique() = %#v, want %#v", got, want)
	}
}
