package plan

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestQuoteFindsTheSpan(t *testing.T) {
	text := "## 1. Outcome\n\nA store.\n\n## 3. Open Questions\n\n* Which store?\n\n"
	qs := structureOfItems(fakes.PlanItems(text)).OpenQuestions
	assert.Assert(t, cmp.Len(qs, 1))
	got := Quote(text, "", qs[0].Blocks)
	assert.Check(t, cmp.Equal(got, "Which store?"))
}

func TestQuoteFallsBackToSources(t *testing.T) {
	blocks := []Block{
		{Type: "paragraph", Text: "One."},
		{Type: "paragraph", Text: "Two."},
	}
	got := Quote("## 1. Outcome\n\nOther.\n", "", blocks)
	assert.Check(t, cmp.Equal(got, "One.\n\nTwo."))
}

func TestQuoteSpansSeveralBlocks(t *testing.T) {
	text := "## 1. Outcome\n\nOne.\n\nBetween.\n\nTwo.\n"
	blocks := []Block{
		{Type: "paragraph", Text: "One."},
		{Type: "paragraph", Text: "Two."},
	}
	got := Quote(text, "", blocks)
	assert.Check(t, cmp.Equal(got, "One.\n\nBetween.\n\nTwo."))
}

func TestQuoteSearchesAfterTheHeading(t *testing.T) {
	const heading = "## 3. Open Questions"
	text := "## 2. Design\n\n* Which store?\n\n" + heading + "\n\n* Which store?\n\n"
	qs := structureOfItems(fakes.PlanItems(text)).OpenQuestions
	assert.Assert(t, cmp.Len(qs, 1))
	got := Quote(text, heading, qs[0].Blocks)
	assert.Check(t, cmp.Equal(got, "Which store?"))

	// Blocks a span apart tell the two places apart by what lies between
	// them: the quote is the one under the heading.
	text = "## 2. Design\n\nOne.\n\nin design\n\nTwo.\n\n" + heading + "\n\nOne.\n\nunder the heading\n\nTwo.\n"
	blocks := []Block{
		{Type: "paragraph", Text: "One."},
		{Type: "paragraph", Text: "Two."},
	}
	got = Quote(text, heading, blocks)
	h := strings.Index(text, heading) + len(heading)
	want := text[h+strings.Index(text[h:], "One."):]
	want = want[:strings.Index(want, "Two.")+len("Two.")]
	assert.Check(t, cmp.Equal(got, want))

	got = Quote(text, "", blocks)
	assert.Check(t, cmp.Equal(got, "One.\n\nin design\n\nTwo."), "Quote with no heading should find the first span")
}
