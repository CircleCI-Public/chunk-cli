package watch

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/onboarding"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/onboarding/onboardingtest"
)

var ctrlP = tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}

// fakeOnboarding is the onboarding timeline from fakes: an org resolved, a
// Setup run detecting commands, no plan to open, and st saved.
func fakeOnboarding(t *testing.T, st onboarding.State) onboarding.Model {
	t.Helper()
	return onboarding.New(t.Context(), onboarding.Deps{
		Root:    "/src/chunk-cli",
		Setup:   onboardingtest.NewSetup(onboardingtest.Found(chunkd.SetupActivity{State: chunkd.SetupRunning, Step: "config", Checks: -1})),
		Account: onboardingtest.NewAccount("org-1", nil, nil),
		Open:    onboardingtest.NoPlan,
		State:   onboardingtest.NewState(st),
	})
}

// onboardingModel is a dashboard opened in /src/chunk-cli on onboarding, with
// an empty flow list behind it.
func onboardingModel(t *testing.T, st onboarding.State) Model {
	t.Helper()
	return sized(planModel(t, &fakePlans{}).WithOnboarding(fakeOnboarding(t, st)))
}

// onboardingFocus is onboarding's ▶ row.
func onboardingFocus(m Model) string {
	for _, l := range strings.Split(ansi.Strip(m.onboarding.Content()), "\n") {
		if strings.HasPrefix(l, "▶ ") {
			return strings.TrimSpace(strings.TrimPrefix(l, "▶ "))
		}
	}
	return ""
}

func TestOnboardingHandsOverToTheFlowList(t *testing.T) {
	keyboardUsed := onboarding.State{Suggestions: []onboarding.Suggestion{
		{
			Title: "Add keyboard shortcuts",
			Used:  true,
		},
		{
			Title: "Export a table to CSV",
		},
		{
			Title: "Add dark mode",
		},
	}}
	tests := []struct {
		name  string
		state onboarding.State
		do    func(t *testing.T, m Model) Model
		check func(t *testing.T, m Model)
	}{
		{
			name: "open, View shows it",
			do:   func(_ *testing.T, m Model) Model { return m },
			check: func(t *testing.T, m Model) {
				assert.Check(t, cmp.Equal(m.onboardingOpen, true))
				assert.Check(t, cmp.Equal(m.flowsOpen, false))
				assert.Check(t, cmp.Contains(screen(m), "What should we change first?"))
				assert.Check(t, cmp.Contains(screen(m), "◆ chunk watch"), "the header is not drawn")
				assert.Check(t, cmp.Equal(m.View().WindowTitle, "chunk watch — set up"))
			},
		},
		{
			name: "LeaveMsg gives the flow list",
			do: func(t *testing.T, m Model) Model {
				next, cmd := m.Update(onboarding.LeaveMsg{})
				return deliverThen(t, next.(Model), cmd, 1)
			},
			check: func(t *testing.T, m Model) {
				assert.Check(t, cmp.Equal(m.flowsOpen, true))
				assert.Check(t, cmp.Equal(m.onboardingOpen, false))
				assert.Check(t, cmp.Nil(m.onboarding))
				s := screen(m)
				assert.Check(t, !strings.Contains(s, "What should we change first?"), s)
				assert.Check(t, cmp.Equal(m.View().WindowTitle, "chunk watch — flows"))
			},
		},
		{
			name:  "DoneMsg gives the flow list with the unused suggestions",
			state: keyboardUsed,
			do: func(t *testing.T, m Model) Model {
				next, cmd := m.Update(onboarding.DoneMsg{})
				return deliverThen(t, next.(Model), cmd, 1)
			},
			check: func(t *testing.T, m Model) {
				s := screen(m)
				assert.Check(t, cmp.Equal(m.flowsOpen, true))
				assert.Check(t, cmp.Equal(m.onboardingOpen, false))
				assert.Check(t, cmp.Contains(s, "Start a change"))
				assert.Check(t, cmp.Contains(s, "Export a table to CSV"))
				assert.Check(t, cmp.Contains(s, "Add dark mode"))
				assert.Check(t, !strings.Contains(s, "Add keyboard shortcuts"), s)
			},
		},
		{
			name: "ctrl+p asks to go to flows",
			do: func(_ *testing.T, m Model) Model {
				m, _ = pressKey(m, ctrlP)
				return m
			},
			check: func(t *testing.T, m Model) {
				assert.Check(t, cmp.Equal(m.onboardingOpen, true))
				assert.Check(t, cmp.Equal(m.flowsOpen, false))
				assert.Check(t, cmp.Contains(screen(m), "Go to flows?"))
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.do(t, onboardingModel(t, tc.state))
			tc.check(t, m)
		})
	}
}

// Onboarding asks before leaving, so ctrl+p then enter saves Left and
// shows the flow list.
func TestOnboardingLeavesThroughItsConfirmation(t *testing.T) {
	store := onboardingtest.NewState(onboarding.State{})
	o := onboarding.New(t.Context(), onboarding.Deps{
		Root:    "/src/chunk-cli",
		Setup:   onboardingtest.NewSetup(),
		Account: onboardingtest.NewAccount("org-1", nil, nil),
		Open:    onboardingtest.NoPlan,
		State:   store,
	})
	m := sized(planModel(t, &fakePlans{}).WithOnboarding(o))
	m, _ = pressKey(m, ctrlP)
	m, cmd := pressKey(m, enter)
	m = deliver(t, m, cmd)
	assert.Check(t, cmp.Equal(m.flowsOpen, true))
	assert.Check(t, cmp.Equal(m.onboardingOpen, false))
	assert.Check(t, cmp.Equal(store.Saved().Left, true))
}
