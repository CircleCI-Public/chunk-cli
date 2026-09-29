package reviewprogress

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
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
	assert.Equal(t, m.rows[0].name, "api-design")
	assert.Equal(t, m.rows[0].state, review.StateQueued)
	assert.Equal(t, m.rows[1].name, "error-handling")
	assert.Equal(t, m.rows[1].state, review.StateQueued)
}

func TestProgressMsgUpdatesRow(t *testing.T) {
	m := send(New(basePrompts(), 2, func() {}),
		ProgressMsg(review.ProgressEvent{Prompt: "api-design", SidecarID: "sb-abc123", State: review.StateRunning}),
		ProgressMsg(review.ProgressEvent{Prompt: "error-handling", SidecarID: "sb-def456", State: review.StateRunning}),
		ProgressMsg(review.ProgressEvent{Prompt: "api-design", SidecarID: "sb-abc123", State: review.StateDone, Duration: 3 * time.Second}),
		ProgressMsg(review.ProgressEvent{Prompt: "error-handling", SidecarID: "sb-def456", State: review.StateFailed, Error: "claude exited 1"}),
	)

	assert.Equal(t, m.rows[0].state, review.StateDone)
	assert.Equal(t, m.rows[0].duration, 3*time.Second)
	assert.Equal(t, m.rows[1].state, review.StateFailed)
	assert.Equal(t, m.rows[1].errMsg, "claude exited 1")
}

func TestDoneMsgQuits(t *testing.T) {
	m := New(basePrompts(), 2, func() {})
	_, cmd := m.Update(DoneMsg{})
	assert.Assert(t, cmd != nil, "DoneMsg should return tea.Quit cmd")
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
