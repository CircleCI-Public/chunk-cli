package fakes

import (
	"regexp"
	"strings"

	"github.com/yuin/goldmark/v2"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/text"
)

// FactoryItems is the items attribute of a plan's structured content, as
// factory serves it.
type FactoryItems struct {
	Outcome       *FactorySection       `json:"outcome"`
	Design        *FactorySection       `json:"design"`
	OpenQuestions *FactoryOpenQuestions `json:"open_questions"`
	WorkedExample *FactorySection       `json:"worked_example"`
	Units         *[]FactoryUnitItem    `json:"units"`
}

// FactorySection is one section of items: its body as written and the
// blocks it parses into.
type FactorySection struct {
	Text   string         `json:"text"`
	Blocks []FactoryBlock `json:"blocks"`
}

// FactoryOpenQuestions is items.open_questions: the section as written, and
// its list's items. It has no blocks of its own.
type FactoryOpenQuestions struct {
	Text      string            `json:"text"`
	Questions []FactoryQuestion `json:"questions"`
}

// FactoryQuestion is one item of 3. Open Questions: its text, without the
// list marker, and the blocks it parses into.
type FactoryQuestion struct {
	Text   string         `json:"text"`
	Blocks []FactoryBlock `json:"blocks"`
}

// FactoryUnitItem is one unit of items.units.
type FactoryUnitItem struct {
	Number int            `json:"number"` // position, from 1
	Name   string         `json:"name"`
	IsDone bool           `json:"is_done"`
	Text   string         `json:"text"`
	Blocks []FactoryBlock `json:"blocks"`
}

// FactoryBlock is one markdown block of items; Blocks is set only on a list,
// a list item and a blockquote.
type FactoryBlock struct {
	Type   string          `json:"type"`
	Text   string          `json:"text"`
	Blocks *[]FactoryBlock `json:"blocks,omitempty"`
}

var (
	// factoryDone is the suffix of a done unit's heading, as factory's
	// plan/structure matches it.
	factoryDone = regexp.MustCompile(` \(done(, [^)]+)?\)$`)
	// factoryUnitNum is the "Unit N: " prefix of a unit's heading; it also
	// matches a heading that is the prefix alone, "Unit 1:".
	factoryUnitNum = regexp.MustCompile(`^Unit \d+:( |$)`)
	// factoryMarker is a list item's marker.
	factoryMarker = regexp.MustCompile(`^([-*+]|\d+[.)])[ \t]*`)
)

// planSkeleton are the headings of the sections items serves, in plan order.
var planSkeleton = [...]string{"1. Outcome", "2. Design", "3. Open Questions", "4. Worked example", "5. Units"}

// PlanItems parses plan text into the items factory serves, with goldmark as
// factory's plan/structure does. A skeleton heading the text has exactly once
// owns the bytes from the end of its line to the next heading of its level or
// higher; one it has none of, or two, is null. A "## " inside a code fence is
// no heading.
func PlanItems(text string) FactoryItems {
	var items FactoryItems
	src := []byte(text)
	doc := goldmark.DefaultParser().Parse(newReader(src))
	var heads []*ast.Heading
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		if h, ok := n.(*ast.Heading); ok {
			heads = append(heads, h)
		}
	}
	bodies := map[string]string{}
	count := map[string]int{}
	for i, h := range heads {
		name := headingText(h, src)
		if h.Level != 2 || !isSkeleton(name) {
			continue
		}
		count[name]++
		bodies[name] = bodyAfter(text, heads, i)
	}
	section := func(name string) *FactorySection {
		if count[name] != 1 {
			return nil
		}
		return &FactorySection{Text: bodies[name], Blocks: blocksOf(bodies[name])}
	}
	items.Outcome = section("1. Outcome")
	items.Design = section("2. Design")
	items.WorkedExample = section("4. Worked example")
	if s := section("3. Open Questions"); s != nil {
		items.OpenQuestions = &FactoryOpenQuestions{Text: s.Text, Questions: questionsOf(s.Blocks)}
	}
	if count["5. Units"] == 1 {
		units := unitsOf(bodies["5. Units"])
		items.Units = &units
	}
	return items
}

func isSkeleton(name string) bool {
	for _, s := range planSkeleton {
		if s == name {
			return true
		}
	}
	return false
}

// unitItem is the unit of items.units with heading name and body text.
func unitItem(name, body string) FactoryUnitItem {
	done := factoryDone.MatchString(name)
	name = factoryDone.ReplaceAllString(name, "")
	name = strings.TrimSpace(factoryUnitNum.ReplaceAllString(name, ""))
	return FactoryUnitItem{Name: name, IsDone: done, Text: body, Blocks: blocksOf(body)}
}

func newReader(src []byte) text.Reader { return text.NewReader(src) }

// headingText is the text of heading h, without its markers.
func headingText(h *ast.Heading, src []byte) string {
	var parts []string
	lines := h.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		parts = append(parts, strings.TrimSpace(string(seg.Value(src))))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// lineStart is the offset of the start of the line holding offset pos.
func lineStart(s string, pos int) int { return strings.LastIndexByte(s[:pos], '\n') + 1 }

// bodyAfter is the text from the end of heads[i]'s line to the start of the
// next heading of its level or higher.
func bodyAfter(s string, heads []*ast.Heading, i int) string {
	pos := heads[i].Pos()
	start := len(s)
	if nl := strings.IndexByte(s[pos:], '\n'); nl >= 0 {
		start = pos + nl + 1
	}
	stop := len(s)
	for _, h := range heads[i+1:] {
		if h.Level <= heads[i].Level {
			stop = lineStart(s, h.Pos())
			break
		}
	}
	if stop < start {
		stop = start
	}
	return s[start:stop]
}

// unitsOf is the units of a 5. Units body: its level-3 headings, each owning
// up to the next.
func unitsOf(body string) []FactoryUnitItem {
	src := []byte(body)
	doc := goldmark.DefaultParser().Parse(newReader(src))
	var heads []*ast.Heading
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		if h, ok := n.(*ast.Heading); ok && h.Level == 3 {
			heads = append(heads, h)
		}
	}
	units := make([]FactoryUnitItem, 0, len(heads))
	for i, h := range heads {
		u := unitItem(headingText(h, src), bodyAfter(body, heads, i))
		u.Number = i + 1
		units = append(units, u)
	}
	return units
}

// questionsOf is the items of the top-level lists of blocks, each with its
// marker and trailing blanks dropped from its text and its own blocks, empty
// items skipped.
func questionsOf(blocks []FactoryBlock) []FactoryQuestion {
	qs := []FactoryQuestion{}
	for _, b := range blocks {
		if b.Type != "list" || b.Blocks == nil {
			continue
		}
		for _, it := range *b.Blocks {
			q := strings.TrimSpace(factoryMarker.ReplaceAllString(it.Text, ""))
			if q == "" {
				continue
			}
			children := []FactoryBlock{}
			if it.Blocks != nil {
				children = *it.Blocks
			}
			qs = append(qs, FactoryQuestion{Text: q, Blocks: children})
		}
	}
	return qs
}

// blocksOf is the blocks of markdown body.
func blocksOf(body string) []FactoryBlock {
	doc := goldmark.DefaultParser().Parse(newReader([]byte(body)))
	return blocksIn(body, doc, len(body))
}

// blocksIn is the blocks under parent, as factory's plan/structure cuts them:
// each runs from its start to the start of the next sibling, or to end.
func blocksIn(body string, parent ast.Node, end int) []FactoryBlock {
	blocks := []FactoryBlock{}
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		if n.Kind() == ast.KindLinkReferenceDefinition {
			continue
		}
		to := end
		if next := n.NextSibling(); next != nil {
			to = next.Pos()
		}
		from := n.Pos()
		if n.Kind() == ast.KindCodeBlock {
			from = strings.LastIndexByte(body[:from], '\n') + 1
		}
		b := FactoryBlock{Text: strings.TrimRight(body[from:to], " \t\r\n")}
		switch n.(type) {
		case *ast.Heading:
			b.Type = "heading"
		case *ast.Paragraph, *ast.TextBlock:
			b.Type = "paragraph"
		case *ast.List, *ast.ListItem, *ast.Blockquote:
			b.Type = map[ast.NodeKind]string{ast.KindList: "list", ast.KindListItem: "list_item", ast.KindBlockquote: "blockquote"}[n.Kind()]
			children := blocksIn(body, n, to)
			b.Blocks = &children
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			b.Type = "code_block"
		case *ast.ThematicBreak:
			b.Type = "thematic_break"
		case *ast.HTMLBlock:
			b.Type = "html"
		}
		blocks = append(blocks, b)
	}
	return blocks
}
