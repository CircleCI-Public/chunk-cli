package plan

import (
	"regexp"
	"strings"
)

// Unit is one of a plan's units: its heading's text, and whether factory has
// marked it done.
type Unit struct {
	// Name is the heading's text without its "### " and its done suffix.
	Name string
	Done bool
}

// unitsHeading is the heading of the section that holds a plan's units.
const unitsHeading = "## 5. Units"

// doneSuffix ends a done unit's heading, as factory's plan done writes it:
// " (done)", or " (done, SHA)" as an older daemon wrote it.
var doneSuffix = regexp.MustCompile(` \(done(, [^)]+)?\)$`)

// Units is text's units, in order, by factory's rules: each "### " line in
// the body of "## 5. Units", outside code fences. A line starting "```"
// opens or closes a fence; headings inside one neither start nor end a
// section.
func Units(text string) []Unit {
	var out []Unit
	fenced, inUnits := false, false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") {
			inUnits = strings.TrimSpace(line) == unitsHeading
			continue
		}
		if heading, ok := strings.CutPrefix(line, "### "); ok && inUnits {
			name, done := unitName(strings.TrimSpace(heading))
			out = append(out, Unit{Name: name, Done: done})
		}
	}
	return out
}

// unitName is a unit heading's text without its done suffix, and whether it
// had one.
func unitName(text string) (string, bool) {
	loc := doneSuffix.FindStringIndex(text)
	if loc == nil {
		return strings.TrimSpace(text), false
	}
	return strings.TrimSpace(text[:loc[0]]), true
}
