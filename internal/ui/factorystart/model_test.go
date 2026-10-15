package factorystart

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"
)

var (
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
	esc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	ctrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	q     = tea.KeyPressMsg{Code: 'q', Text: "q"}
)

func update(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	got, ok := next.(model)
	assert.Assert(t, ok)
	return got, cmd
}

// sendPrompt types prompt and presses Enter, then runs the work the model
// starts, as the program would, and delivers its result.
func sendPrompt(t *testing.T, m model, prompt string) model {
	t.Helper()
	m.input.SetValue(prompt)
	m, _ = update(t, m, enter)
	assert.Equal(t, m.state, stateWorking)
	m, _ = update(t, m, m.run(prompt)())
	return m
}

func TestHandingOffAPass(t *testing.T) {
	var got string
	m := newModel(t.Context(), func(_ context.Context, prompt string, activity func(string)) (Pass, error) {
		got = prompt
		activity("Edit main.go")
		return Pass{RunID: "run-1", Summary: "Added the flag", Stat: " main.go | 2 +-\n 1 file changed"}, nil
	})

	m = sendPrompt(t, m, "add a --verbose flag")
	assert.Equal(t, got, "add a --verbose flag")
	assert.Equal(t, m.state, stateDone)
	view := m.View().Content
	assert.Assert(t, strings.Contains(view, "Added the flag"), view)
	assert.Assert(t, strings.Contains(view, "main.go | 2 +-"), view)
	assert.Assert(t, strings.Contains(view, "enter hand off to the factory"), view)

	m, cmd := update(t, m, enter)
	assert.Assert(t, cmd != nil, "enter quits")
	assert.Equal(t, m.outcome.Choice, HandOff)
	assert.Equal(t, m.outcome.Pass.RunID, "run-1")
}

func TestKeepingAPass(t *testing.T) {
	m := newModel(t.Context(), func(context.Context, string, func(string)) (Pass, error) {
		return Pass{RunID: "run-1", Stat: " main.go | 1 +"}, nil
	})
	m = sendPrompt(t, m, "add a flag")

	m, _ = update(t, m, q)
	assert.Equal(t, m.outcome.Choice, Keep)
}

// There is nothing for the factory to refine without a diff, so a pass with
// no changes can only be left.
func TestAPassWithNoChangesCannotBeHandedOff(t *testing.T) {
	m := newModel(t.Context(), func(context.Context, string, func(string)) (Pass, error) {
		return Pass{RunID: "run-1", Summary: "Nothing to do"}, nil
	})
	m = sendPrompt(t, m, "add a flag")
	assert.Assert(t, strings.Contains(m.View().Content, "Claude made no changes."))

	m, cmd := update(t, m, enter)
	assert.Assert(t, cmd == nil, "enter does nothing")
	assert.Equal(t, m.state, stateDone)

	m, _ = update(t, m, q)
	assert.Equal(t, m.outcome.Choice, Keep)
}

func TestTypingThePrompt(t *testing.T) {
	m := newModel(t.Context(), nil)
	for _, r := range "add a flag" {
		m, _ = update(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	assert.Equal(t, m.input.Value(), "add a flag")
}

func TestAnEmptyPromptIsNotSent(t *testing.T) {
	m := newModel(t.Context(), func(context.Context, string, func(string)) (Pass, error) {
		t.Fatal("no work for an empty prompt")
		return Pass{}, nil
	})
	m.input.SetValue("   ")

	m, _ = update(t, m, enter)
	assert.Equal(t, m.state, statePrompt)
}

func TestLeavingBeforeSendingAPrompt(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{esc, ctrlC} {
		m := newModel(t.Context(), nil)
		m, cmd := update(t, m, key)
		assert.Assert(t, cmd != nil, "%s quits", key)
		assert.Equal(t, m.outcome.Choice, Cancelled)
	}
}

// Ctrl-C while the agent works stops it, and the run as far as it got is
// what the developer is told about.
func TestStoppingTheAgent(t *testing.T) {
	m := newModel(t.Context(), func(ctx context.Context, _ string, _ func(string)) (Pass, error) {
		<-ctx.Done()
		return Pass{RunID: "run-1", Worktree: "/tmp/run-1"}, ctx.Err()
	})
	m.input.SetValue("add a flag")
	m, _ = update(t, m, enter)
	work := m.run("add a flag")

	m, _ = update(t, m, ctrlC)
	assert.Assert(t, m.stopping)
	assert.Assert(t, strings.Contains(m.View().Content, "Stopping Claude"))

	m, cmd := update(t, m, work())
	assert.Assert(t, cmd != nil, "a stopped pass quits")
	assert.Equal(t, m.outcome.Choice, Failed)
	assert.Assert(t, errors.Is(m.outcome.Err, context.Canceled))
	assert.Equal(t, m.outcome.Pass.RunID, "run-1")
}

func TestTheLatestActivityIsShown(t *testing.T) {
	m := newModel(t.Context(), nil)
	m.state = stateWorking
	for _, a := range []string{"Read a.go", "Read b.go", "Read c.go", "Read d.go", "Edit a.go", "Edit b.go"} {
		m, _ = update(t, m, activityMsg(a))
	}
	view := m.View().Content
	assert.Assert(t, !strings.Contains(view, "Read a.go"), view)
	assert.Assert(t, strings.Contains(view, "Edit b.go"), view)
}

func TestClipKeepsTheTotals(t *testing.T) {
	stat := strings.Join([]string{" a | 1 +", " b | 1 +", " c | 1 +", " d | 1 +", " 4 files changed"}, "\n")

	assert.Equal(t, clip(stat, 5), stat)
	assert.Equal(t, clip(stat, 4), " a | 1 +\n b | 1 +\n … 2 more\n 4 files changed")
}
