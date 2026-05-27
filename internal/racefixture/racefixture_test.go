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
			name: "mixed sign",
			a:    -4,
			b:    9,
			want: 5,
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
