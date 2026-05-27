package racefixture

import "testing"

func TestSum(t *testing.T) {
	tests := []struct {
		name string
		a, b int
		want int
	}{
		{
			name: "positive numbers",
			a:    1,
			b:    2,
			want: 3,
		},
		{
			name: "zero values",
			a:    0,
			b:    0,
			want: 0,
		},
		{
			name: "negative numbers",
			a:    -5,
			b:    -3,
			want: -8,
		},
		{
			name: "mixed signs",
			a:    10,
			b:    -7,
			want: 3,
		},
		{
			name: "large numbers",
			a:    1000000,
			b:    2000000,
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
}

func TestMultiply(t *testing.T) {
	if got := Multiply(3, 4); got != 12 {
		t.Fatalf("Multiply(3, 4) = %d, want 12", got)
	}
}
