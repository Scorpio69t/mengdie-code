package sum

import "testing"

func TestSumToIncludesUpperBoundary(t *testing.T) {
	if got := SumTo(3); got != 6 {
		t.Fatalf("SumTo(3) = %d, want 6", got)
	}
}

func TestSumToNegative(t *testing.T) {
	if got := SumTo(-1); got != 0 {
		t.Fatalf("SumTo(-1) = %d, want 0", got)
	}
}
