package watch

import (
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

func attentionRun(id string, state chunkd.SessionState, loop chunkd.StageState, prStatus string) sessionInfo {
	r := liveSession(id, state, time.Now())
	r.s.Stages[0].State = loop
	r.s.Factory = &chunkd.FactoryRun{Prompt: id}
	if prStatus != "" {
		r.s.Factory.PullRequest = &chunkd.PullRequest{State: chunkd.PROpened, Number: 1, URL: "https://example.com/pr/1", Status: prStatus}
	}
	return r
}

func TestAttentionViewListsOnlyRunsNeedingAPerson(t *testing.T) {
	runs := []sessionInfo{
		attentionRun("running", chunkd.SessionRunning, chunkd.StageRunning, ""),
		attentionRun("passed", chunkd.SessionDone, chunkd.StageDone, chunkd.PRStatusInReview),
		attentionRun("failed", chunkd.SessionFailed, chunkd.StageFailed, ""),
		attentionRun("cancelled", chunkd.SessionCancelled, chunkd.StageFailed, ""),
		attentionRun("loop", chunkd.SessionDone, chunkd.StageFailed, ""),
		attentionRun("feedback", chunkd.SessionDone, chunkd.StageDone, chunkd.PRStatusFeedback),
		attentionRun("checks", chunkd.SessionDone, chunkd.StageDone, chunkd.PRStatusChecksFailing),
		attentionRun("merged", chunkd.SessionDone, chunkd.StageDone, chunkd.PRStatusMerged),
	}
	m := sessModel(runs...)
	m.showEnded = false
	m.sessions = m.listedRuns()

	m, _ = press(m, 'a')
	var ids []string
	for _, r := range m.sessions {
		ids = append(ids, r.s.ID)
	}
	assert.Check(t, cmp.DeepEqual(ids, []string{"failed", "loop", "feedback", "checks"}))
	out := strings.Join(m.renderRunList(newWatchStyles(true)), "\n")
	assert.Check(t, cmp.Contains(out, "needs attention (4)"))

	m, _ = press(m, 'a')
	assert.Check(t, !m.attention)
	assert.Assert(t, cmp.Len(m.sessions, 2), "back to the live runs, with the selected run kept")
	assert.Check(t, cmp.Equal(m.sessions[0].s.ID, "running"))
}

func TestAttentionViewSaysWhenNothingNeedsYou(t *testing.T) {
	m := sessModel(attentionRun("passed", chunkd.SessionDone, chunkd.StageDone, chunkd.PRStatusMerged))
	m, _ = press(m, 'a')
	assert.Check(t, cmp.Len(m.sessions, 0))
	out := strings.Join(m.renderRunList(newWatchStyles(true)), "\n")
	assert.Check(t, cmp.Contains(out, "nothing needs you"))
}

func TestClickingTheHeaderTagNarrowsTheList(t *testing.T) {
	m := sessModel(
		attentionRun("passed", chunkd.SessionDone, chunkd.StageDone, chunkd.PRStatusMerged),
		attentionRun("failed", chunkd.SessionFailed, chunkd.StageFailed, ""),
	)
	start, end := m.attentionTagSpan(m.styles())
	assert.Assert(t, end > start, "the tag is in the header")

	m = clickAt(m, start-1, 0)
	assert.Check(t, !m.attention, "beside the tag")
	m = clickAt(m, start, 1)
	assert.Check(t, !m.attention, "not on the header row")
	m = clickAt(m, start, 0)
	assert.Check(t, m.attention)
	assert.Check(t, cmp.Len(m.sessions, 1))
	m = clickAt(m, start, 0)
	assert.Check(t, !m.attention)
}
