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
			name: "mixed positive and negative",
			a:    10,
			b:    -3,
			want: 7,
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

func TestDivide(t *testing.T) {
	tests := []struct {
		name        string
		a           int
		b           int
		want        int
		expectError bool
		errorMsg    string
	}{
		{
			name:        "normal division",
			a:           10,
			b:           2,
			want:        5,
			expectError: false,
		},
		{
			name:        "division by zero",
			a:           5,
			b:           0,
			want:        0,
			expectError: true,
			errorMsg:    "cannot divide 5 by zero: division by zero is undefined",
		},
		{
			name:        "negative numbers",
			a:           -10,
			b:           2,
			want:        -5,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Divide(tt.a, tt.b)
			if tt.expectError {
				if err == nil {
					t.Fatalf("Divide(%d, %d) expected error but got none", tt.a, tt.b)
				}
				if err.Error() != tt.errorMsg {
					t.Errorf("Divide(%d, %d) error = %q, want %q", tt.a, tt.b, err.Error(), tt.errorMsg)
				}
			} else {
				if err != nil {
					t.Fatalf("Divide(%d, %d) unexpected error: %v", tt.a, tt.b, err)
				}
				if got != tt.want {
					t.Errorf("Divide(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
				}
			}
		})
	}
}
