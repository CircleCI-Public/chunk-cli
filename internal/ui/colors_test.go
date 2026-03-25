package ui

import "testing"

func TestWrapWithColorsEnabled(t *testing.T) {
	SetColorEnabled(true)
	t.Cleanup(func() { SetColorEnabled(false) })

	got := Red("hello")
	want := "\x1b[31mhello\x1b[0m"
	if got != want {
		t.Errorf("Red(\"hello\") = %q, want %q", got, want)
	}
}

func TestWrapWithColorsDisabled(t *testing.T) {
	SetColorEnabled(false)

	tests := []struct {
		name string
		fn   func(string) string
	}{
		{"Red", Red},
		{"Green", Green},
		{"Yellow", Yellow},
		{"Cyan", Cyan},
		{"Gray", Gray},
		{"Bold", Bold},
		{"Dim", Dim},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn("text")
			if got != "text" {
				t.Errorf("%s(\"text\") with NO_COLOR = %q, want \"text\"", tt.name, got)
			}
		})
	}
}

func TestAllColorCodes(t *testing.T) {
	SetColorEnabled(true)
	t.Cleanup(func() { SetColorEnabled(false) })

	tests := []struct {
		name string
		fn   func(string) string
		code string
	}{
		{"Red", Red, "31"},
		{"Green", Green, "32"},
		{"Yellow", Yellow, "33"},
		{"Cyan", Cyan, "36"},
		{"Gray", Gray, "90"},
		{"Bold", Bold, "1"},
		{"Dim", Dim, "2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn("x")
			want := "\x1b[" + tt.code + "mx\x1b[0m"
			if got != want {
				t.Errorf("%s(\"x\") = %q, want %q", tt.name, got, want)
			}
		})
	}
}
