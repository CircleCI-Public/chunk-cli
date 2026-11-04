package onboarding

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// questionCard is one item of 3. Open Questions, with an answer input.
type questionCard struct {
	n        int
	q        plan.Question
	quote    string
	answered bool
	changed  bool
	saving   bool
	err      error
	input    textinput.Model
	d        cardData
}

func newQuestionCard(n int) questionCard {
	return questionCard{n: n, input: newInput("your answer…")}
}

func (c questionCard) ID() string      { return questionIDPrefix + strconv.Itoa(c.n) }
func (c questionCard) Needs() bool     { return !c.answered }
func (c questionCard) typed() string   { return typedText(c.input) }
func (c questionCard) capturing() bool { return false }

func (c questionCard) Update(msg tea.Msg) (card, tea.Cmd) {
	switch msg := msg.(type) {
	case refreshMsg:
		var q plan.Question
		if c.n < len(msg.structure.OpenQuestions) {
			q = msg.structure.OpenQuestions[c.n]
		}
		if c.q.Text != "" && q.Text != c.q.Text && c.typed() != "" {
			c.changed = true
		}
		c.q, c.d = q, msg.cardData
		c.quote = plan.Quote(msg.text, questionsAfter, q.Blocks)
		c.answered = c.quote != "" && slices.ContainsFunc(msg.comments, func(cm plan.Comment) bool {
			return !cm.Resolved && cm.Quote == c.quote
		})
		c.input.SetWidth(max(msg.width-6, 1))
	case focusMsg:
		if msg.focused && !c.answered {
			_ = c.input.Focus()
		} else {
			c.input.Blur()
		}
	case tea.KeyPressMsg:
		if c.answered {
			return c, nil
		}
		if msg.String() != keyEnter {
			var cmd tea.Cmd
			c.input, cmd = c.input.Update(msg)
			return c, cmd
		}
		body := strings.TrimSpace(c.input.Value())
		if body == "" || c.saving {
			return c, nil
		}
		c.saving, c.err = true, nil
		ctx, s, blocks := c.d.ctx, c.d.store, c.q.Blocks
		return c, c.d.wrap(c.ID(), func() tea.Msg {
			text, err := s.Text(ctx)
			if err != nil {
				return answerSavedMsg{err: fmt.Errorf("read the plan: %w", err)}
			}
			if _, err := s.AddComment(ctx, plan.Quote(text, questionsAfter, blocks), body); err != nil {
				return answerSavedMsg{err: fmt.Errorf("save the answer: %w", err)}
			}
			return answerSavedMsg{}
		})
	case tea.PasteMsg:
		if !c.answered {
			var cmd tea.Cmd
			c.input, cmd = c.input.Update(msg)
			return c, cmd
		}
	case answerSavedMsg:
		c.saving, c.err = false, msg.err
		if msg.err == nil {
			c.input.Reset()
			c.input.Blur()
			c.changed, c.answered = false, true
		}
	}
	return c, nil
}

func (c questionCard) View(width int, focused bool) string {
	st := c.d.styles
	lines := []string{}
	if c.answered {
		lines = append(lines, head(st, actDone, "Answer a question", ownerYou, "answered"))
	} else {
		lines = append(lines, head(st, actYou, "Answer a question", ownerYou, ""))
	}
	lines = append(lines, wrapText(c.q.Text, width)...)
	if !c.answered {
		lines = append(lines, "  "+st.Field.Render(c.input.View()))
	}
	if c.changed {
		lines = append(lines, "  "+st.Warning.Render("(question changed)"))
	}
	if c.saving {
		lines = append(lines, "  "+st.Dim.Render("saving…"))
	}
	if c.err != nil {
		lines = append(lines, "  "+st.Error.Render(oneLine(c.err.Error())))
	}
	if focused && !c.answered {
		lines = append(lines, "  "+st.Dim.Render("enter save answer · ctrl+enter send answers"))
	}
	return strings.Join(lines, "\n")
}
