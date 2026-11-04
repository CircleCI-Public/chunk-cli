package watch

import (
	"image/color"

	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/flowlist"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/onboarding"
)

// WithOnboarding returns a copy of m that opens on o, the onboarding
// timeline, instead of the flow list. It shows o in the main pane, beside
// the flow list and plan screens: o sits under the header beside the chat as
// the flow list does, and hands over to the flow list when it is left or
// done.
func (m Model) WithOnboarding(o onboarding.Model) Model {
	if !m.hasDarkBG {
		o, _ = o.Update(tea.BackgroundColorMsg{Color: color.White})
	}
	m.onboarding = &o
	m.onboardingOpen, m.flowsOpen = true, false
	return m
}

// Suggestions is st's unused suggestions, as the flow list offers them under
// "Start a change".
func Suggestions(st onboarding.State) []flowlist.Suggestion {
	unused := st.Unused()
	out := make([]flowlist.Suggestion, 0, len(unused))
	for _, t := range unused {
		out = append(out, flowlist.Suggestion{Title: t})
	}
	return out
}

// forwardOnboarding hands msg to the onboarding timeline, when there is one.
func (m Model) forwardOnboarding(msg tea.Msg) (Model, tea.Cmd) {
	if m.onboarding == nil {
		return m, nil
	}
	var cmd tea.Cmd
	*m.onboarding, cmd = m.onboarding.Update(msg)
	return m, cmd
}

// closeOnboarding drops the timeline and shows the flow list, with the
// suggestions onboarding left unused under "Start a change".
func (m Model) closeOnboarding() (Model, tea.Cmd) {
	if m.onboarding == nil {
		return m, nil
	}
	st := m.onboarding.State()
	m.onboarding, m.onboardingOpen = nil, false
	m.flowsOpen = true
	if m.flows == nil {
		return m, nil
	}
	*m.flows = m.flows.WithSuggestions(Suggestions(st))
	return m, m.flows.Refresh()
}
