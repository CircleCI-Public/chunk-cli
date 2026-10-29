package flowlist

import (
	"strings"
	"testing"
)

// BenchmarkRenderPlan is one glamour render of a plan-sized document.
// Over 16ms (one frame), it would stall the UI goroutine.
func BenchmarkRenderPlan(b *testing.B) {
	section := "## Units\n\n### Unit 1: x\n\n1. `a.go`: change b\n\n```go\nfunc f() {}\n```\n\n- bullet with `code` and **bold**\n\n"
	text := "# plan\n\n" + strings.Repeat(section, 40)
	for b.Loop() {
		_ = glamourLines(text, 80, true)
	}
}
