package flowlist

import "github.com/CircleCI-Public/chunk-cli/internal/ui/plan"

var Phase = phase

var Capitalize = capitalize

// WithRender has m render plan structure with r, so a test can count renders.
func WithRender(m Model, r func(s plan.Structure, width int, dark bool) []string) Model {
	m.render = r
	return m
}
