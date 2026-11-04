package flowlist

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

const planURL = "https://app.circleci.com/factory/github/circleci/flows/164a2947-74a7-44d9-9708-c8ea7284dcba"

func TestPlanLinesShowALink(t *testing.T) {
	s := plan.Structure{Sections: []plan.Section{{Heading: "1. Outcome", Blocks: []plan.Block{
		{Type: "paragraph", Text: "Builds after [plan-id-links](" + planURL + ")."},
	}}}}
	for _, dark := range []bool{true, false} {
		out := strings.Join(plan.Lines(s, 80, dark), "\n")
		plain := ansi.Strip(out)
		assert.Check(t, cmp.Contains(plain, "plan-id-links"))
		assert.Check(t, !strings.ContainsRune(plain, '\x1b'), "no escape is left after stripping")
	}
}

// BenchmarkRenderPlan is one draw of a plan-sized structure.
// Over 16ms (one frame), it would stall the UI goroutine.
func BenchmarkRenderPlan(b *testing.B) {
	unit := plan.Unit{Number: 1, Name: "x", Blocks: []plan.Block{
		{Type: "list_item", Text: "1. `a.go`: change b"},
		{Type: "code_block", Text: "```go\nfunc f() {}\n```"},
		{Type: "list_item", Text: "- bullet with `code` and **bold**"},
	}}
	s := plan.Structure{Units: make([]plan.Unit, 40)}
	for i := range s.Units {
		s.Units[i] = unit
	}
	for b.Loop() {
		_ = plan.Lines(s, 80, true)
	}
}
