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
			name: "zero and positive",
			a:    0,
			b:    5,
			want: 5,
		},
		{
			name: "negative numbers",
			a:    -3,
			b:    -2,
			want: -5,
		},
		{
			name: "positive and negative",
			a:    10,
			b:    -4,
			want: 6,
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
