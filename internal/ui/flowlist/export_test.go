package flowlist

var Phase = phase

var Capitalize = capitalize

// WithRender has m render plan text with r, so a test can count renders.
func WithRender(m Model, r func(text string, width int, dark bool) []string) Model {
	m.render = r
	return m
}
