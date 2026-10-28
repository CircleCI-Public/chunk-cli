package plan

import "strings"

// Render draws a plan's markdown as the planning screen's plan pane does:
// sections set apart under a rule, units as steps, and the rest in the same
// style, as lines at most width columns wide. It is for showing a plan that is
// not being edited, such as one a run was started from.
func Render(md string, dark bool, width int) []string {
	if strings.TrimSpace(md) == "" || width < 1 {
		return nil
	}
	m := Model{dark: dark}
	r, err := newRenderer(dark, width-1)
	if err != nil {
		return strings.Split(md, "\n")
	}
	var (
		lines []string
		st    planStats
		prev  block
	)
	for i, b := range splitBlocks(md) {
		bl, ok := m.styled(b, &st, width)
		if !ok {
			bl, ok = m.blockLines(b, width-1)
		}
		if !ok {
			bl = renderLines(r, b.source)
		}
		if i > 0 && !joinsPrevious(prev, b) {
			lines = append(lines, "")
		}
		lines = append(lines, bl...)
		prev = b
	}
	return lines
}
