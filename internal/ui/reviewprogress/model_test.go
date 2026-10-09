package reviewprogress

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/promptrows"
)

func basePrompts() []review.Prompt {
	return []review.Prompt{
		{Name: "api-design", Body: "x"},
		{Name: "error-handling", Body: "x"},
	}
}

func send(m Model, msgs ...tea.Msg) Model {
	var tm tea.Model = m
	for _, msg := range msgs {
		tm, _ = tm.Update(msg)
	}
	return tm.(Model)
}

func TestNewModelInitialState(t *testing.T) {
	m := New(basePrompts(), 2, func() {})

	assert.Equal(t, len(m.rows), 2)
	assert.Equal(t, m.rows[0].Name, "api-design")
	assert.Equal(t, m.rows[0].State, promptrows.Queued)
	assert.Equal(t, m.rows[1].Name, "error-handling")
	assert.Equal(t, m.rows[1].State, promptrows.Queued)
}

func TestProgressMsgUpdatesRow(t *testing.T) {
	m := send(New(basePrompts(), 2, func() {}),
		ProgressMsg(review.ProgressEvent{Prompt: "api-design", SidecarID: "sb-abc123", State: review.StateRunning}),
		ProgressMsg(review.ProgressEvent{Prompt: "error-handling", SidecarID: "sb-def456", State: review.StateRunning}),
		ProgressMsg(review.ProgressEvent{Prompt: "api-design", SidecarID: "sb-abc123", State: review.StateDone, Duration: 3 * time.Second}),
		ProgressMsg(review.ProgressEvent{Prompt: "error-handling", SidecarID: "sb-def456", State: review.StateFailed, Error: "claude exited 1"}),
	)

	assert.Equal(t, m.rows[0].State, promptrows.Done)
	assert.Equal(t, m.rows[0].Duration, 3*time.Second)
	assert.Equal(t, m.rows[1].State, promptrows.Failed)
	assert.Equal(t, m.rows[1].Err, "claude exited 1")
}

func TestDoneMsgQuits(t *testing.T) {
	m := New(basePrompts(), 2, func() {})
	_, cmd := m.Update(DoneMsg{})
	assert.Assert(t, cmd != nil, "DoneMsg should return tea.Quit cmd")
}

func TestDoneMsgWithErrorSettlesUnfinishedRows(t *testing.T) {
	m := send(New(basePrompts(), 1, func() {}),
		ProgressMsg(review.ProgressEvent{Prompt: "api-design", SidecarID: "sb-abc123", State: review.StateRunning}),
		DoneMsg{Err: review.ErrClaudeMissing},
	)

	for _, r := range m.rows {
		assert.Equal(t, r.State, promptrows.Failed, "row %s", r.Name)
		assert.Equal(t, r.Err, review.ErrClaudeMissing.Error())
	}
	assert.Assert(t, !strings.Contains(m.render(), "running"))
}

func TestRenderFailedErrorStaysOnOneLine(t *testing.T) {
	m := send(New(basePrompts(), 2, func() {}),
		tea.WindowSizeMsg{Width: 80, Height: 24},
		ProgressMsg(review.ProgressEvent{Prompt: "api-design", State: review.StateFailed,
			Error: "claude exited 1: warning: foo\nError: " + strings.Repeat("é", 60)}),
	)

	got := m.render()
	assert.Assert(t, !strings.Contains(got, "\nError:"), "error text broke onto a new line:\n%s", got)
	assert.Assert(t, strings.Contains(got, "warning: foo Error:"))
	assert.Assert(t, utf8.ValidString(got))
}

func TestQuitKeyCancels(t *testing.T) {
	cancelled := false
	m := New(basePrompts(), 2, func() { cancelled = true })
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q'})
	assert.Assert(t, cancelled, "q should call cancelFn")
	assert.Assert(t, cmd != nil, "q should return a quit cmd")
}

func TestRenderShowsAllPrompts(t *testing.T) {
	m := send(New(basePrompts(), 2, func() {}), tea.WindowSizeMsg{Width: 80, Height: 24})

	got := m.render()
	assert.Assert(t, strings.Contains(got, "api-design"), "render: %s", got)
	assert.Assert(t, strings.Contains(got, "error-handling"), "render: %s", got)
	assert.Assert(t, strings.Contains(got, "chunk review"), "render: %s", got)
}

func TestRenderCountsInSummaryBar(t *testing.T) {
	m := send(New(basePrompts(), 2, func() {}),
		tea.WindowSizeMsg{Width: 80, Height: 24},
		ProgressMsg(review.ProgressEvent{Prompt: "api-design", State: review.StateRunning}),
	)

	got := m.render()
	assert.Assert(t, strings.Contains(got, "1 running"), "render: %s", got)
	assert.Assert(t, strings.Contains(got, "1 queued"), "render: %s", got)
}
