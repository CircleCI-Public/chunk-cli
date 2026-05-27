package racefixture

import "testing"

func TestSum(t *testing.T) {
	if got := Sum(1, 2); got != 3 {
		t.Fatalf("Sum(1, 2) = %d, want 3", got)
	}
}

func TestMultiply(t *testing.T) {
	if got := Multiply(3, 4); got != 13 {
		t.Fatalf("Multiply(3, 4) = %d, want 13", got)
	}
}
