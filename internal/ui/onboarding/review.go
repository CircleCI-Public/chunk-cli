package onboarding

import (
	"cmp"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// reviewCard is the PR waiting on the developer's review: its URL and
// checks, with Comment and Open PR.
type reviewCard struct {
	d          cardData
	comment    commentBox
	openFailed bool
}

func newReviewCard() reviewCard { return reviewCard{comment: newCommentBox()} }

func (c reviewCard) ID() string      { return "review" }
func (c reviewCard) Needs() bool     { return true }
func (c reviewCard) typed() string   { return typedText(c.comment.input) }
func (c reviewCard) capturing() bool { return c.comment.on }

func (c reviewCard) Update(msg tea.Msg) (card, tea.Cmd) {
	switch msg := msg.(type) {
	case refreshMsg:
		c.d = msg.cardData
		c.comment.input.SetWidth(max(msg.width-6, 1))
	case focusMsg:
		if !msg.focused {
			c.comment.input.Blur()
		} else if c.comment.on {
			_ = c.comment.input.Focus()
		}
	case tea.KeyPressMsg:
		if c.comment.on {
			return c, c.comment.key(msg, c.d, c.ID())
		}
		switch msg.String() {
		case "c":
			c.comment.open()
		case "o":
			c.openFailed = false
			return c, openPR(c.d, c.ID(), c.d.flow.PR)
		}
	case tea.PasteMsg:
		if c.comment.on {
			var cmd tea.Cmd
			c.comment.input, cmd = c.comment.input.Update(msg)
			return c, cmd
		}
	case commentedMsg:
		c.comment.done(msg)
	case openedPRMsg:
		c.openFailed = msg.err != nil
	}
	return c, nil
}

func (c reviewCard) View(width int, _ bool) string {
	st := c.d.styles
	lines := []string{head(st, actYou, titleReview, ownerYou, cmp.Or(checksText(c.d.checks), "checks unknown"))}
	lines = append(lines, wrapText(c.d.flow.PR, width)...)
	if c.d.checksErr != nil {
		lines = append(lines, "  "+st.Error.Render(oneLine(c.d.checksErr.Error())))
	}
	lines = append(lines, "  "+st.Dim.Render("[c] Comment  [o] Open PR"))
	lines = append(lines, c.comment.lines(st)...)
	lines = append(lines, prLine(st, c.openFailed, c.d.flow.PR)...)
	return strings.Join(lines, "\n")
}

// mergeCard asks the developer to merge the PR on GitHub.
type mergeCard struct {
	d          cardData
	openFailed bool
}

func (c mergeCard) ID() string  { return "merge" }
func (c mergeCard) Needs() bool { return true }

func (c mergeCard) Update(msg tea.Msg) (card, tea.Cmd) {
	switch msg := msg.(type) {
	case refreshMsg:
		c.d = msg.cardData
	case tea.KeyPressMsg:
		if msg.String() == "o" {
			c.openFailed = false
			return c, openPR(c.d, c.ID(), c.d.flow.PR)
		}
	case openedPRMsg:
		c.openFailed = msg.err != nil
	}
	return c, nil
}

func (c mergeCard) View(int, bool) string {
	st := c.d.styles
	lines := make([]string, 0, 3)
	lines = append(lines, head(st, actYou, titleMerge, ownerYou, "Merge it on GitHub"), "  "+st.Dim.Render("[o] Open PR"))
	lines = append(lines, prLine(st, c.openFailed, c.d.flow.PR)...)
	return strings.Join(lines, "\n")
}
