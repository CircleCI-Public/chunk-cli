package ui

import "testing"

func TestColorEnabled_disabledReturnsPlain(t *testing.T) {
	SetColorEnabled(false)
	t.Cleanup(func() { SetColorEnabled(false) })

	fns := map[string]func(string) string{
		"Red": Red, "Green": Green, "Yellow": Yellow,
		"Cyan": Cyan, "Gray": Gray, "Bold": Bold, "Dim": Dim,
	}
	for name, fn := range fns {
		got := fn("text")
		if got != "text" {
			t.Errorf("%s with colors disabled = %q, want plain", name, got)
		}
	}
}

func TestColorEnabled_enabledWraps(t *testing.T) {
	SetColorEnabled(true)
	t.Cleanup(func() { SetColorEnabled(false) })

	got := Green("hello")
	if got == "hello" {
		t.Errorf("Green with colors enabled should add styling, got plain")
	}
}
