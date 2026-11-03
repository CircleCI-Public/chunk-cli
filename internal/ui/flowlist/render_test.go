package flowlist

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

const planURL = "https://app.circleci.com/factory/github/circleci/flows/164a2947-74a7-44d9-9708-c8ea7284dcba"

func TestGlamourLinesShowsALink(t *testing.T) {
	text := "Builds after [plan-id-links](" + planURL + ")."
	for _, dark := range []bool{true, false} {
		out := strings.Join(glamourLines(text, 80, dark), "\n")
		plain := ansi.Strip(out)
		assert.Check(t, cmp.Contains(plain, "plan-id-links"))
		assert.Check(t, !strings.ContainsRune(plain, '\x1b'), "no escape is left after stripping")
	}
}

// BenchmarkRenderPlan is one glamour render of a plan-sized document.
// Over 16ms (one frame), it would stall the UI goroutine.
func BenchmarkRenderPlan(b *testing.B) {
	section := "## Units\n\n### Unit 1: x\n\n1. `a.go`: change b\n\n```go\nfunc f() {}\n```\n\n- bullet with `code` and **bold**\n\n"
	text := "# plan\n\n" + strings.Repeat(section, 40)
	for b.Loop() {
		_ = glamourLines(text, 80, true)
	}
}
