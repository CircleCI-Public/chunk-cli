package racefixture

import "testing"

func TestSum(t *testing.T) {
	if got := Sum(1, 2); got != 3 {
		t.Fatalf("Sum(1, 2) = %d, want 3", got)
	}

	// Table-driven subtest for comprehensive coverage
	t.Run("table-driven", func(t *testing.T) {
		tests := []struct {
			name string
			a, b int
			want int
		}{
			{
				name: "positive numbers",
				a:    5, b: 7,
				want: 12,
			},
			{
				name: "negative numbers",
				a:    -3, b: -5,
				want: -8,
			},
			{
				name: "mixed sign",
				a:    10, b: -4,
				want: 6,
			},
			{
				name: "zero values",
				a:    0, b: 0,
				want: 0,
			},
			{
				name: "large numbers",
				a:    1000000, b: 2000000,
				want: 3000000,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := Sum(tt.a, tt.b); got != tt.want {
					t.Errorf("Sum(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
				}
			})
		}
	})
}

func TestMultiply(t *testing.T) {
	if got := Multiply(3, 4); got != 12 {
		t.Fatalf("Multiply(3, 4) = %d, want 12", got)
	}
}

func TestCalculate(t *testing.T) {
	// Calculate(1, 2, 3) should return (1+2)*3 = 9
	got, err := Calculate(1, 2, 3)
	if err != nil {
		t.Fatalf("Calculate(1, 2, 3) unexpected error: %v", err)
	}
	if got != 9 {
		t.Fatalf("Calculate(1, 2, 3) = %d, want 9", got)
	}

	// Test error case: factor of zero should return error
	_, err = Calculate(1, 2, 0)
	if err == nil {
		t.Fatalf("Calculate(1, 2, 0) expected error, got nil")
	}
}
