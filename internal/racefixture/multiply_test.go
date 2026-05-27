package racefixture

import "testing"

func TestMultiply(t *testing.T) {
	if got := Multiply(3, 4); got != 12 {
		t.Fatalf("Multiply(3, 4) = %d, want 12", got)
	}
}

func TestMultiplyTable(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{2, 3, 6},
		{0, 5, 0},
	}
	for _, tc := range tests {
		if got := Multiply(tc.a, tc.b); got != tc.want {
			t.Fatalf("Multiply(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
