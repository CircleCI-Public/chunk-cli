package racefixture

import (
	"math"
	"strings"
	"testing"
)

func TestSum(t *testing.T) {
	tests := []struct {
		name string
		a    int
		b    int
		want int
	}{
		{
			name: "positive numbers",
			a:    1,
			b:    2,
			want: 3,
		},
		{
			name: "zero addition",
			a:    5,
			b:    0,
			want: 5,
		},
		{
			name: "negative numbers",
			a:    -3,
			b:    -7,
			want: -10,
		},
		{
			name: "mixed signs",
			a:    10,
			b:    -4,
			want: 6,
		},
		{
			name: "both zero",
			a:    0,
			b:    0,
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sum(tt.a, tt.b); got != tt.want {
				t.Errorf("Sum(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestMultiply(t *testing.T) {
	got, err := Multiply(3, 4)
	if err != nil {
		t.Fatalf("Multiply(3, 4) returned unexpected error: %v", err)
	}
	if got != 12 {
		t.Fatalf("Multiply(3, 4) = %d, want 12", got)
	}
}

func TestMultiplyOverflow(t *testing.T) {
	// Test overflow case
	_, err := Multiply(math.MaxInt, 2)
	if err == nil {
		t.Fatal("expected overflow error for Multiply(MaxInt, 2), got nil")
	}
	if !strings.Contains(err.Error(), "multiplication overflow") {
		t.Errorf("expected overflow error message, got: %v", err)
	}
}
