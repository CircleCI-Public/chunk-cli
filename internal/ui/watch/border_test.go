package watch

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"gotest.tools/v3/assert"
)

// The frame around the panes is red, and the only way to tell is to read the
// colour out of the escape sequences: every other test here strips them, and
// lipgloss.Width ignores them, so a frame drawn in any other colour would pass
// the whole suite. The assertion is that red dominates rather than that a
// particular hex was used, so a later shade change does not have to touch it.

// cell is one visible rune of a rendered line with the foreground colour that
// was in effect when it was written, as the "r;g;b" of a truecolor SGR.
type cell struct {
	r   rune
	col int
	fg  string
}

// cells splits a rendered line into its visible runes, tracking the foreground
// colour set by the escape sequences between them.
func cells(line string) []cell {
	var out []cell
	fg, col := "", 0
	for i := 0; i < len(line); {
		if line[i] == '\x1b' {
			end := strings.IndexByte(line[i:], 'm')
			if end < 0 {
				break
			}
			fg = applySGR(fg, line[i+2:i+end])
			i += end + 1
			continue
		}
		r, size := decodeRune(line[i:])
		out = append(out, cell{r: r, col: col, fg: fg})
		col += lipgloss.Width(string(r))
		i += size
	}
	return out
}

// applySGR folds one escape sequence's parameters into the foreground colour,
// which is all this test reads.
func applySGR(fg, params string) string {
	if params == "" || params == "0" || params == "39" {
		return ""
	}
	if after, ok := strings.CutPrefix(params, "38;2;"); ok {
		return after
	}
	return fg
}

func decodeRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 1
}

// isRed reports whether a foreground is a red: bright enough to read, with the
// red channel well clear of the other two.
func isRed(fg string) bool {
	parts := strings.Split(fg, ";")
	if len(parts) != 3 {
		return false
	}
	var rgb [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return false
		}
		rgb[i] = v
	}
	return rgb[0] >= 128 && rgb[0] > rgb[1]+50 && rgb[0] > rgb[2]+50
}

func TestRender_frameIsRed(t *testing.T) {
	for _, dark := range []bool{true, false} {
		for _, focus := range []pane{paneLeft, paneRight} {
			m := sessionModel("", []sidecarInfo{
				{id: "id1", sessionID: "sessA", repoName: "repo", branch: "main",
					lastActivity: time.Now()},
			})
			m.width, m.height = 100, 24
			m.hasDarkBG = dark
			m.focusedPane = focus
			m.selectedIdx = 0

			lines := strings.Split(strings.TrimRight(m.render(), "\n"), "\n")
			top, bottom := -1, -1
			for i, line := range lines {
				switch {
				case strings.Contains(stripANSI(line), "╭"):
					top = i
				case strings.Contains(stripANSI(line), "╰"):
					bottom = i
				}
			}
			assert.Assert(t, top >= 0 && bottom > top, "no frame in:\n%s", stripANSI(m.render()))

			// The top and bottom edges are frame end to end.
			for _, i := range []int{top, bottom} {
				for _, c := range cells(lines[i]) {
					assert.Assert(t, isRed(c.fg),
						"dark=%v focus=%v line %d: %q at column %d is %q, want a red",
						dark, focus, i, string(c.r), c.col, c.fg)
				}
			}

			// Between them, the frame is the two outer edges and the divider;
			// everything else on the line is pane content.
			for i := top + 1; i < bottom; i++ {
				cs := cells(lines[i])
				assert.Assert(t, len(cs) > 0, "line %d is empty", i)
				edges := []cell{cs[0], cs[len(cs)-1]}
				for _, c := range cs {
					if c.col == leftPaneWidth+2 {
						edges = append(edges, c)
					}
				}
				assert.Equal(t, len(edges), 3, "line %d: no divider at column %d", i, leftPaneWidth+2)
				for _, c := range edges {
					assert.Assert(t, c.r == '│', "line %d column %d: want a frame edge, got %q", i, c.col, string(c.r))
					assert.Assert(t, isRed(c.fg),
						"dark=%v focus=%v line %d: edge at column %d is %q, want a red",
						dark, focus, i, c.col, c.fg)
				}
			}
		}
	}
}

// The unfocused pane's edge is dimmer, but it is still red: dimming it to the
// old grey would leave half the frame off-colour on every frame drawn.
func TestWatchStyles_bothBorderShadesAreRed(t *testing.T) {
	for _, dark := range []bool{true, false} {
		st := newWatchStyles(dark)
		for name, style := range map[string]lipgloss.Style{"Border": st.Border, "BorderDim": st.BorderDim} {
			cs := cells(style.Render("─"))
			assert.Equal(t, len(cs), 1)
			assert.Assert(t, isRed(cs[0].fg), "dark=%v: %s is %q, want a red", dark, name, cs[0].fg)
		}
	}
}
