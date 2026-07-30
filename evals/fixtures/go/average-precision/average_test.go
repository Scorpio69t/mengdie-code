package average

import "testing"

func TestAverageKeepsFraction(t *testing.T) {
	if got := Average([]int{1, 2}); got != 1.5 {
		t.Fatalf("Average() = %v, want 1.5", got)
	}
}

func TestAverageEmpty(t *testing.T) {
	if got := Average(nil); got != 0 {
		t.Fatalf("Average(nil) = %v, want 0", got)
	}
}
