package racefixture

import "testing"

func TestSum(t *testing.T) {
	// Keep the original simple test case
	if got := Sum(1, 2); got != 3 {
		t.Fatalf("Sum(1, 2) = %d, want 3", got)
	}

	// Add table-driven subtests
	tests := []struct {
		name string
		a    int
		b    int
		want int
	}{
		{
			name: "positive_numbers",
			a:    5,
			b:    7,
			want: 12,
		},
		{
			name: "negative_numbers",
			a:    -3,
			b:    -4,
			want: -7,
		},
		{
			name: "mixed_positive_negative",
			a:    10,
			b:    -4,
			want: 6,
		},
		{
			name: "zero_values",
			a:    0,
			b:    0,
			want: 0,
		},
		{
			name: "one_zero",
			a:    42,
			b:    0,
			want: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Sum(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("Sum(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestMultiply(t *testing.T) {
	if got := Multiply(3, 4); got != 12 {
		t.Fatalf("Multiply(3, 4) = %d, want 12", got)
	}
}
