package theme

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestNewUsesTheBrandAccent(t *testing.T) {
	if got := New(true).Teal.GetForeground(); got != Yellow {
		t.Errorf("dark Teal foreground = %v, want Yellow", got)
	}
	if got := New(false).Teal.GetForeground(); got != Navy {
		t.Errorf("light Teal foreground = %v, want Navy", got)
	}
	if got := New(true).Accent.GetBackground(); got != Yellow {
		t.Errorf("dark Accent background = %v, want Yellow", got)
	}
	if got := New(false).Accent.GetBackground(); got != Navy {
		t.Errorf("light Accent background = %v, want Navy", got)
	}
}

func TestEdgeSegment(t *testing.T) {
	line := func(s string) string { return s }

	if got := EdgeSegment(line, "title", 11); got != "─ title ───" {
		t.Errorf("fitting title: got %q", got)
	}

	long := EdgeSegment(line, "a very long title that cannot fit", 11)
	if lipgloss.Width(long) != 11 {
		t.Errorf("long title: width = %d, want 11: %q", lipgloss.Width(long), long)
	}
	if !strings.HasSuffix(long, "─") {
		t.Errorf("long title leaves no trailing edge: %q", long)
	}

	for _, n := range []int{0, 1, 2, 3, 4} {
		if got := EdgeSegment(line, "title", n); got != strings.Repeat("─", n) {
			t.Errorf("n=%d: got %q, want a plain edge", n, got)
		}
	}
	if got := EdgeSegment(line, "", 11); got != strings.Repeat("─", 11) {
		t.Errorf("empty title: got %q, want a plain edge", got)
	}

	for _, n := range []int{5, 6, 7, 11, 20} {
		if got := lipgloss.Width(EdgeSegment(line, "title", n)); got != n {
			t.Errorf("n=%d: width = %d, want %d", n, got, n)
		}
	}
}

func TestPane(t *testing.T) {
	st := New(true)
	for _, dims := range []struct{ w, h int }{{20, 6}, {30, 10}, {10, 5}} {
		body := strings.Join(bodyLines(dims.w-2, dims.h-2), "\n")
		out := st.Pane(true, dims.w, dims.h, "title", body)
		lines := strings.Split(out, "\n")
		if len(lines) != dims.h {
			t.Errorf("w=%d h=%d: got %d lines, want %d", dims.w, dims.h, len(lines), dims.h)
		}
		for i, l := range lines {
			if got := lipgloss.Width(l); got != dims.w {
				t.Errorf("w=%d h=%d: line %d width = %d, want %d: %q", dims.w, dims.h, i, got, dims.w, l)
			}
		}
	}

	body := strings.Repeat("x", 18)
	out := st.Pane(true, 20, 6, "title", body+"\n"+strings.Join(bodyLines(18, 3), "\n"))
	lines := strings.Split(out, "\n")
	if ansi.Strip(lines[1]) != "│"+body+"│" {
		t.Errorf("a body line exactly w-2 wide was rewrapped: %q", lines[1])
	}

	if got := ansi.Strip(strings.Split(st.Pane(true, 20, 6, "title", strings.Join(bodyLines(18, 4), "\n")), "\n")[0]); got != "╭─ title ──────────╮" {
		t.Errorf("stripped title line = %q", got)
	}

	focused := st.Pane(true, 20, 6, "t", strings.Join(bodyLines(18, 4), "\n"))
	unfocused := st.Pane(false, 20, 6, "t", strings.Join(bodyLines(18, 4), "\n"))
	if want := st.Teal.Render("╭"); !strings.HasPrefix(focused, want) {
		t.Errorf("focused pane does not start with the accent ╭: %q", focused)
	}
	if want := st.VDim.Render("╭"); !strings.HasPrefix(unfocused, want) {
		t.Errorf("unfocused pane does not start with the vdim ╭: %q", unfocused)
	}
}

// bodyLines returns n lines, each exactly w cells wide.
func bodyLines(w, n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = strings.Repeat("x", w)
	}
	return lines
}
