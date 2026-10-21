package watch

import (
	tea "charm.land/bubbletea/v2"
)

// Continuer starts a factory run that picks up the work of run runID in the
// project at root, as `chunk factory build --continue <run>` does with no
// prompt, and returns the new run's session ID. Errors are shown as typed, so
// they should read as a sentence.
type Continuer func(root, runID string) (string, error)

// runContinuedMsg reports the outcome of continuing the run whose session is
// from.
type runContinuedMsg struct {
	from string
	id   string
	err  error
}

// WithContinue returns a copy of m on which r continues the selected run with
// cont, when it ended with committed work that has not passed its checks.
func (m Model) WithContinue(cont Continuer) Model {
	m.continuer = cont
	return m
}

// canContinue reports whether run can be continued from the dashboard.
func (m Model) canContinue(run *sessionInfo) bool {
	return m.continuer != nil && !m.continuing && run != nil && run.s.State.Finished() &&
		run.s.Factory != nil && run.s.Factory.Continuable()
}

// continueRun starts a run that picks up run's work. The new run checks the
// work first and sends the implementer what failed, as --continue does
// without a prompt.
func (m Model) continueRun(run *sessionInfo) (tea.Model, tea.Cmd) {
	m.runNote = "starting a run to continue this one…"
	m.continuing = true
	cont, root, from, runID := m.continuer, run.s.ProjectRoot, run.s.ID, run.s.Factory.ContinueID()
	return m, func() tea.Msg {
		id, err := cont(root, runID)
		return runContinuedMsg{from: from, id: id, err: err}
	}
}

// withRunContinued notes how continuing a run went. The new run is selected
// once a poll lists it, with the search cleared so it is not filtered out.
func (m Model) withRunContinued(msg runContinuedMsg) Model {
	m.continuing = false
	if msg.err != nil {
		if run := m.selectedRun(); run != nil && run.s.ID == msg.from {
			m.runNote = "could not continue the run: " + msg.err.Error()
		}
		return m
	}
	m.pendingRun = msg.id
	m.focusedPane = paneLeft
	m = m.setQuery("")
	m.runNote = "continuing in a new run; it appears in the list in a moment"
	return m
}
