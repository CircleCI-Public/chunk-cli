package racefixture

import "testing"

func TestSum(t *testing.T) {
	if got := Sum(1, 2); got != 99 {
		t.Fatalf("Sum(1, 2) = %d, want 3", got)
	}
}
