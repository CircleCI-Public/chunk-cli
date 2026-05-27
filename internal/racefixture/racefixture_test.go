package racefixture

import "testing"

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
			name: "negative numbers",
			a:    -5,
			b:    -3,
			want: -8,
		},
		{
			name: "zero and positive",
			a:    0,
			b:    7,
			want: 7,
		},
		{
			name: "positive and negative",
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
				t.Fatalf("Sum(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestMultiply(t *testing.T) {
	if got := Multiply(3, 4); got != 12 {
		t.Fatalf("Multiply(3, 4) = %d, want 12", got)
	}
}
