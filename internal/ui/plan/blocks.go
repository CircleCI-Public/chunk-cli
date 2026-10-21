package plan

import (
	"regexp"
	"strings"
)

// blockKind is what a block of the plan is, which decides what joins it and
// how it is spaced from the block before it.
type blockKind int

const (
	kindParagraph blockKind = iota
	kindHeading
	kindListItem
	kindCode
	kindRule
)

// block is one piece of the plan the developer can comment on: a heading, a
// paragraph, one item of a list (with any list nested under it), a code
// block, a table or a quote. source is its markdown, exactly as in the plan
// file, so a comment can quote it and Claude find it.
type block struct {
	kind   blockKind
	source string
}

var (
	headingRe  = regexp.MustCompile(`^ {0,3}#{1,6}(\s|$)`)
	listItemRe = regexp.MustCompile(`^ {0,1}([-*+]|\d{1,9}[.)])(\s|$)`)
	fenceRe    = regexp.MustCompile("^\\s*(```+|~~~+)")
	ruleRe     = regexp.MustCompile(`^ {0,3}((\*\s*){3,}|(-\s*){3,}|(_\s*){3,})$`)
)

// splitBlocks splits a plan's markdown into blocks. It reads markdown the way
// plans are written, not every corner of CommonMark: what it splits wrongly
// is only commented on as a larger or smaller piece.
func splitBlocks(md string) []block {
	var (
		blocks []block
		cur    []string
		kind   blockKind
		// blank is whether a blank line has been seen since cur's last line.
		blank bool
		// fence is the open code fence's marker, or "" outside one.
		fence string
	)
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, block{kind: kind, source: strings.TrimRight(strings.Join(cur, "\n"), "\n ")})
		}
		cur, blank = nil, false
	}
	start := func(k blockKind, line string) {
		flush()
		kind, cur = k, []string{line}
	}

	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		if fence != "" {
			cur = append(cur, line)
			if strings.HasPrefix(strings.TrimSpace(line), fence) {
				fence = ""
			}
			continue
		}

		indented := strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")
		inItem := kind == kindListItem && len(cur) > 0

		switch {
		case strings.TrimSpace(line) == "":
			if len(cur) > 0 {
				blank = true
			}
			continue

		// What is indented under a list item belongs to it: its own
		// paragraphs, nested lists and code.
		case inItem && indented:
			if blank {
				cur = append(cur, "")
			}
			cur = append(cur, line)
			blank = false
			if m := fenceRe.FindStringSubmatch(line); m != nil {
				fence = m[1]
			}

		case fenceRe.MatchString(line):
			start(kindCode, line)
			fence = fenceRe.FindStringSubmatch(line)[1]

		case headingRe.MatchString(line):
			start(kindHeading, line)
			flush()

		case ruleRe.MatchString(line):
			start(kindRule, line)
			flush()

		case listItemRe.MatchString(line):
			start(kindListItem, line)

		// A line straight after a paragraph or item, with no blank line
		// between, carries it on.
		case len(cur) > 0 && !blank && kind != kindCode:
			cur = append(cur, line)

		default:
			start(kindParagraph, line)
		}
	}
	flush()
	return blocks
}

// joinsPrevious is whether b follows prev with no blank line, as the items of
// one list do.
func joinsPrevious(prev, b block) bool {
	return prev.kind == kindListItem && b.kind == kindListItem
}
