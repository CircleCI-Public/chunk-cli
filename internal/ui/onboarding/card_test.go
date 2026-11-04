package onboarding_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/onboarding"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/onboarding/onboardingtest"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

const (
	answerOpenQuestions = "answer-open-questions"
	approveForUnits     = "approve-for-units"
	approveForBuilding  = "approve-for-building"
	prURL               = "https://github.com/o/r/pull/1"
)

const planText = `# Add dark mode

## 1. Outcome

The app has a dark theme.

## 3. Open Questions

* Which store?
* Which color?
`

var (
	outcome = []plan.Block{{Type: "paragraph", Text: "The app has a dark theme."}}
	// questions are the plan's two open questions.
	questions = []plan.Question{
		{Text: "Which store?", Blocks: []plan.Block{{Type: "paragraph", Text: "Which store?"}}},
		{Text: "Which color?", Blocks: []plan.Block{{Type: "paragraph", Text: "Which color?"}}},
	}
)

// steps is a flow's next: each event the developer's, and required.
func steps(events ...string) []plan.Step {
	out := make([]plan.Step, len(events))
	for i, e := range events {
		out[i] = plan.Step{Actor: plan.ActorHuman, Event: e, Required: true}
	}
	return out
}

func structureOf(qs []plan.Question) plan.Structure {
	return plan.Structure{
		Sections:      []plan.Section{{Heading: "1. Outcome", Blocks: outcome}},
		Units:         []plan.Unit{{Number: 1, Name: "theme"}, {Number: 2, Name: "toggle"}},
		OpenQuestions: qs,
	}
}

// planWorld is a reopened plan, set up, whose flow waits on the developer
// for next, with qs open.
func planWorld(next []plan.Step, qs []plan.Question) *world {
	w := newWorld(onboarding.State{PlanID: "plan-9", Suggestion: "Add dark mode"}, onboardingtest.Found(ok(1)))
	w.store.SetFlow(plan.Flow{State: "refine-proposal", WaitingOn: "human", Next: next}, nil)
	w.store.SetStructure(structureOf(qs))
	w.store.SetPlanText(planText)
	return w
}

// written is the calls that write to factory, in order.
func written(s *onboardingtest.Store) []string { return s.Calls() }

func ctrlEnter() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl} }

func TestQuestionCards(t *testing.T) {
	t.Run("answer quotes the question", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(answerOpenQuestions), questions)
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the questions", d.shows("Which color?"))
			content := d.content()
			assert.Check(t, cmp.Contains(content, "⚠ Plan · agent · waits for your answers"))
			d.typeText("Postgres")
			d.press(tea.KeyEnter)
			d.until("saves the answer", func() bool { return len(w.store.Added()) == 1 })
			got := w.store.Added()[0]
			want := plan.Quote(planText, "## 3. Open Questions", questions[0].Blocks)
			assert.Check(t, cmp.Equal(got.Quote, want))
			assert.Check(t, cmp.Equal(got.Quote, "Which store?"))
			assert.Check(t, cmp.Equal(got.Body, "Postgres"))
			d.until("shows it answered", d.shows("✓ Answer a question · you · answered"))
		})
	})

	t.Run("reworded while typing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(answerOpenQuestions), questions[:1])
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the question", d.shows("Which store?"))
			d.typeText("Post")

			reworded := []plan.Question{{Text: "Which database?", Blocks: []plan.Block{{Type: "paragraph", Text: "Which database?"}}}}
			w.store.SetStructure(structureOf(reworded))
			w.store.SetPlanText(strings.Replace(planText, "Which store?", "Which database?", 1))
			d.until("marks it changed", d.shows("(question changed)"))
			content := d.content()
			assert.Check(t, cmp.Contains(content, "Which database?"))
			unsent := d.m.Unsent()
			assert.Check(t, cmp.Equal(unsent, "Post"))
			calls := written(w.store)
			assert.Check(t, cmp.Len(calls, 0))

			d.typeText("gres")
			d.press(tea.KeyEnter)
			d.until("saves the answer", func() bool { return len(w.store.Added()) == 1 })
			got := w.store.Added()[0]
			assert.Check(t, cmp.Equal(got.Quote, "Which database?"))
			assert.Check(t, cmp.Equal(got.Body, "Postgres"))
		})
	})

	t.Run("send answers saves unsaved text first", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(answerOpenQuestions), questions[:1])
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the question", d.shows("Which store?"))
			d.typeText("Postgres")
			d.send(ctrlEnter())
			d.until("sends the answers", func() bool { return slices.Contains(written(w.store), "AnswerOpenQuestions") })
			calls := written(w.store)
			assert.Check(t, cmp.DeepEqual(calls, []string{"AddComment", "AnswerOpenQuestions"}))
			got := w.store.Added()[0]
			assert.Check(t, cmp.Equal(got.Quote, "Which store?"))
			unsent := d.m.Unsent()
			assert.Check(t, cmp.Equal(unsent, ""))
		})
	})

	t.Run("send answers row", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(answerOpenQuestions), questions[:1])
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the question", d.shows("Which store?"))
			content := d.content()
			assert.Check(t, !strings.Contains(content, "Send answers"), content)
			d.typeText("Postgres")
			d.press(tea.KeyEnter)
			d.until("offers Send answers", d.shows("[enter] Send answers"))
			focused := d.focused()
			assert.Check(t, cmp.Contains(focused, "Send answers"))
			d.press(tea.KeyEnter)
			d.until("sends the answers", func() bool { return slices.Contains(written(w.store), "AnswerOpenQuestions") })
			calls := written(w.store)
			assert.Check(t, cmp.DeepEqual(calls, []string{"AddComment", "AnswerOpenQuestions"}))
		})
	})

	t.Run("answered elsewhere, and removed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(answerOpenQuestions), questions)
			w.store.SetComments([]plan.Comment{{ID: "web-1", Quote: "Which store?", Body: "Postgres"}})
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the questions", d.shows("Which color?"))
			content := d.content()
			answered := strings.Count(content, "✓ Answer a question · you · answered")
			assert.Check(t, cmp.Equal(answered, 1), content)
			open := strings.Count(content, "◐ Answer a question · you")
			assert.Check(t, cmp.Equal(open, 1), content)

			w.store.SetStructure(structureOf(questions[:1]))
			d.until("drops the removed question", func() bool { return !d.shows("Which color?")() })
			content = d.content()
			assert.Check(t, cmp.Contains(content, "Which store?"))
		})
	})

	t.Run("a new card leaves focus on typed text", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(answerOpenQuestions), questions[:1])
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the question", d.shows("Which store?"))
			d.typeText("half")

			w.store.SetStructure(structureOf(questions))
			d.until("shows the new question", d.shows("Which color?"))
			content := d.content()
			assert.Check(t, cmp.Contains(content, "◐ Answer a question · you •"))
			d.typeText(" done")
			unsent := d.m.Unsent()
			assert.Check(t, cmp.Equal(unsent, "half done"))
		})
	})
}

func TestApproveCards(t *testing.T) {
	t.Run("design approves", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(approveForUnits), nil)
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the card", d.shows("◐ Approve the design · you"))
			content := d.content()
			assert.Check(t, cmp.Contains(content, "The app has a dark theme."))
			assert.Check(t, cmp.Contains(content, "├ 1. theme"))
			assert.Check(t, cmp.Contains(content, "└ 2. toggle"))
			assert.Check(t, cmp.Contains(content, "⚠ Plan · agent · waits for your approval"))
			d.press('a')
			d.until("approves the design", func() bool { return slices.Contains(written(w.store), "ApproveDesign") })
			calls := written(w.store)
			assert.Check(t, cmp.DeepEqual(calls, []string{"ApproveDesign"}))
		})
	})

	t.Run("design refused with an unsent comment", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(approveForUnits), nil)
			w.store.SetComments([]plan.Comment{{ID: "c1", Quote: "The app has a dark theme.", Body: "why?"}})
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the card", d.shows("◐ Approve the design · you"))
			d.press('a')
			d.until("refuses", d.shows(plan.CommentsFirst))
			d.idle(time.Second)
			calls := written(w.store)
			assert.Check(t, cmp.Len(calls, 0))
		})
	})

	t.Run("design comment", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(approveForUnits), nil)
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the card", d.shows("◐ Approve the design · you"))
			d.press('c')
			d.typeText("make it darker")
			d.press(tea.KeyEnter)
			d.until("sends the comment", func() bool { return slices.Contains(written(w.store), "AddressComments") })
			calls := written(w.store)
			assert.Check(t, cmp.DeepEqual(calls, []string{"AddComment", "AddressComments"}))
			got := w.store.Added()[0]
			want := plan.Quote(planText, "## 1. Outcome", outcome)
			assert.Check(t, cmp.Equal(got.Quote, want))
			assert.Check(t, cmp.Equal(got.Body, "make it darker"))
			unsent := d.m.Unsent()
			assert.Check(t, cmp.Equal(unsent, ""))
		})
	})

	t.Run("design approval fails", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(approveForUnits), nil)
			w.store.Fail("ApproveDesign", errors.New("factory says no"))
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the card", d.shows("◐ Approve the design · you"))
			d.press('c')
			d.typeText("draft")
			d.press(tea.KeyEsc)
			unsent := d.m.Unsent()
			assert.Check(t, cmp.Equal(unsent, "draft"))
			d.press('a')
			d.until("refuses", d.shows(plan.CommentsFirst))
			d.press('c')
			for range len("draft") {
				d.press(tea.KeyBackspace)
			}
			d.press(tea.KeyEsc)
			d.press('a')
			d.until("shows the error", d.shows("factory says no"))
			content := d.content()
			assert.Check(t, cmp.Contains(content, "◐ Approve the design · you"))
			calls := written(w.store)
			assert.Check(t, cmp.DeepEqual(calls, []string{"ApproveDesign"}))
		})
	})

	t.Run("build calls the approver", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(approveForBuilding), nil)
			w.store.SetFlow(plan.Flow{State: "refine-units", WaitingOn: "human", Next: steps(approveForBuilding)}, nil)
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the card", d.shows("◐ Approve the build · you"))
			content := d.content()
			assert.Check(t, cmp.Contains(content, "○ Up next: Build · agent · after your approval"))
			d.press('a')
			d.until("approves", func() bool { return w.opener.Approvals() == 1 })
			d.until("shows it approved", d.shows("approved"))
			calls := written(w.store)
			assert.Check(t, cmp.Len(calls, 0))
		})
	})

	t.Run("build waits for setup", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(approveForBuilding), nil)
			w.setup.Then(onboardingtest.Found(running(1)))
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the card", d.shows("◐ Approve the build · you"))
			content := d.content()
			assert.Check(t, cmp.Contains(content, "○ Up next: Build · agent · after setup and your approval"))
		})
	})

	t.Run("two steps give two cards in order", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(steps(approveForBuilding, approveForUnits), nil)
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows both cards", func() bool {
				return d.shows("Approve the build")() && d.shows("Approve the design")()
			})
			c := d.content()
			build, design := strings.Index(c, "Approve the build"), strings.Index(c, "Approve the design")
			assert.Check(t, build < design, c)
		})
	})
}

func TestReviewCards(t *testing.T) {
	integrate := plan.Flow{State: "integrate", WaitingOn: "human", PR: prURL}

	t.Run("open PR with a failing browser shows the URL", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(nil, nil)
			w.store.SetFlow(integrate, nil)
			var mu sync.Mutex
			var opened []string
			w.browser = func(url string) error {
				mu.Lock()
				defer mu.Unlock()
				opened = append(opened, url)
				return errors.New("no display")
			}
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the card", d.shows("◐ Review · you"))
			d.press('o')
			d.until("shows the URL", d.shows("Open "+prURL+" in your browser"))
			mu.Lock()
			defer mu.Unlock()
			assert.Check(t, cmp.DeepEqual(opened, []string{prURL}))
		})
	})

	t.Run("review comment", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(nil, nil)
			w.store.SetFlow(integrate, nil)
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the card", d.shows("◐ Review · you"))
			d.press('c')
			d.typeText("rename it")
			d.press(tea.KeyEnter)
			d.until("sends the comment", func() bool { return slices.Contains(written(w.store), "AddressComments") })
			calls := written(w.store)
			assert.Check(t, cmp.DeepEqual(calls, []string{"AddComment", "AddressComments"}))
		})
	})

	t.Run("checks passed gives merge", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := planWorld(nil, nil)
			w.store.SetFlow(integrate, nil)
			w.checks = func(_ context.Context, _ string) (*onboarding.Checks, error) {
				return &onboarding.Checks{State: "success", Passed: 3}, nil
			}
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows merge", d.shows("◐ Merge · you · Merge it on GitHub"))
			content := d.content()
			assert.Check(t, cmp.Contains(content, "◐ Review · you · checks passed"))
		})
	})
}
