package onboarding

import (
	"cmp"
	"errors"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// approveCard is the design's approval (approve-for-units) or the build's
// (approve-for-building): 1. Outcome and the units, with Approve, Comment
// and Show plan.
type approveCard struct {
	build    bool
	d        cardData
	showPlan bool
	comment  commentBox
	busy     bool
	approved bool
	refused  string
	err      error
	steps    []string
}

func newApproveCard(build bool) approveCard {
	return approveCard{build: build, comment: newCommentBox()}
}

func (c approveCard) ID() string {
	if c.build {
		return "build"
	}
	return "design"
}

func (c approveCard) Needs() bool     { return true }
func (c approveCard) typed() string   { return typedText(c.comment.input) }
func (c approveCard) capturing() bool { return c.comment.on }

// refusal is why a can't approve now, "" when it can.
func (c approveCard) refusal() string {
	f := c.d.flow
	if !plan.ReadyToApprove(f) {
		return "The plan cannot be approved now: " + cmp.Or(oneLine(f.WaitingOnWhy), "it is "+oneLine(f.State)) + "."
	}
	unsent := c.typed() != ""
	if !c.build {
		unsent = unsent || slices.ContainsFunc(c.d.comments, func(cm plan.Comment) bool { return !cm.Resolved })
	}
	if !unsent {
		return ""
	}
	if c.build {
		return plan.CommentsFirst + "; then a approves the build."
	}
	return plan.CommentsFirst + "; then a approves the design."
}

func (c approveCard) Update(msg tea.Msg) (card, tea.Cmd) {
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
		return c.key(msg.String())
	case tea.PasteMsg:
		if c.comment.on {
			var cmd tea.Cmd
			c.comment.input, cmd = c.comment.input.Update(msg)
			return c, cmd
		}
	case commentedMsg:
		c.comment.done(msg)
	case approvedMsg:
		c.busy, c.err = false, msg.err
		c.approved = msg.err == nil
	case approveStepMsg:
		c.steps = append(c.steps, oneLine(msg.line))
	}
	return c, nil
}

func (c approveCard) key(k string) (card, tea.Cmd) {
	switch k {
	case "a":
		if c.busy || c.approved {
			return c, nil
		}
		c.refused, c.err = c.refusal(), nil
		if c.refused != "" {
			return c, nil
		}
		c.busy, c.steps = true, nil
		if c.build {
			return c, c.approveBuild()
		}
		ctx, s := c.d.ctx, c.d.store
		return c, c.d.wrap(c.ID(), func() tea.Msg { return approvedMsg{err: s.ApproveDesign(ctx)} })
	case "c":
		c.refused = ""
		c.comment.open()
	case "p":
		c.showPlan = !c.showPlan
	}
	return c, nil
}

// approveBuild calls the approver, handing each step it starts to the
// card as it goes.
func (c approveCard) approveBuild() tea.Cmd {
	approve, ctx := c.d.approver, c.d.ctx
	if approve == nil {
		return c.d.wrap(c.ID(), func() tea.Msg { return approvedMsg{err: errors.New("this plan can't be approved here")} })
	}
	steps := make(chan string, 16)
	run := c.d.wrap(c.ID(), func() tea.Msg {
		defer close(steps)
		_, err := approve(func(s string) {
			select {
			case steps <- s:
			case <-ctx.Done():
			}
		})
		return approvedMsg{err: err}
	})
	return tea.Batch(run, listenSteps(c.d, c.ID(), steps))
}

// listenSteps hands the card the approver's next step, then listens again.
func listenSteps(d cardData, id string, steps <-chan string) tea.Cmd {
	ctx := d.ctx
	return func() tea.Msg {
		select {
		case s, ok := <-steps:
			if !ok {
				return cardMsg{store: d.store, id: id, msg: approveStepsEnd{}}
			}
			return tea.BatchMsg{
				func() tea.Msg { return cardMsg{store: d.store, id: id, msg: approveStepMsg{line: s}} },
				listenSteps(d, id, steps),
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (c approveCard) View(width int, _ bool) string {
	st := c.d.styles
	title, detail := "Approve the design", "the units are written next"
	if c.build {
		title, detail = "Approve the build", "chunk builds it next"
	}
	lines := []string{head(st, actYou, title, ownerYou, detail)}
	if c.showPlan {
		for _, l := range plan.Lines(c.d.structure, max(width-4, 2), c.d.dark) {
			lines = append(lines, "    "+l)
		}
	} else {
		for _, b := range outcomeBlocks(c.d.structure) {
			for _, l := range strings.Split(strings.TrimSpace(b.Text), "\n") {
				lines = append(lines, wrapText(l, width)...)
			}
		}
		lines = append(lines, c.unitLines()...)
	}
	lines = append(lines, "  "+st.Dim.Render("[a] Approve  [c] Comment  [p] "+map[bool]string{false: "Show plan", true: "Hide plan"}[c.showPlan]))
	lines = append(lines, c.comment.lines(st)...)
	for _, s := range c.steps {
		lines = append(lines, "  "+st.Dim.Render(s))
	}
	switch {
	case c.busy:
		lines = append(lines, "  "+st.Dim.Render("approving…"))
	case c.approved:
		lines = append(lines, "  "+st.Success.Render("approved"))
	}
	if c.refused != "" {
		lines = append(lines, "  "+st.Warning.Render(c.refused))
	}
	if c.err != nil {
		lines = append(lines, "  "+st.Error.Render(oneLine(c.err.Error())))
	}
	return strings.Join(lines, "\n")
}

// unitLines is the units as a small tree.
func (c approveCard) unitLines() []string {
	units := c.d.structure.Units
	if len(units) == 0 {
		return nil
	}
	out := []string{"  " + c.d.styles.Emphasis.Render("Units")}
	for i, u := range units {
		branch := "├ "
		if i == len(units)-1 {
			branch = "└ "
		}
		out = append(out, "  "+branch+strconv.Itoa(u.Number)+". "+oneLine(u.Name))
	}
	return out
}
