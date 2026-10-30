package watch

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
)

// A header that stayed green over a dead daemon would hide that plans cannot
// be built.
func TestRenderHeader_flagsAnUnreachableDaemon(t *testing.T) {
	m := New().WithConnection(chunkd.Connection{})
	m.width = 100
	m.daemonErr = errors.New("connect to chunk daemon: no such file")

	header := ansi.Strip(m.renderHeader(m.styles()))
	assert.Check(t, cmp.Contains(header, "local unreachable"))
}

// A daemon that answers again clears the header.
func TestDaemonCheckClearsTheError(t *testing.T) {
	m := New().WithConnection(chunkd.Connection{})
	m.width = 100
	m.checkDaemon = func() error { return nil }
	m.daemonErr = errors.New("gone")

	next, tick := m.Update(m.pingDaemon())
	m = next.(Model)
	assert.Check(t, m.daemonErr == nil)
	assert.Check(t, tick != nil, "the next check is scheduled")
	assert.Check(t, cmp.Contains(ansi.Strip(m.renderHeader(m.styles())), "✓ local"))
}

// The header shows no clock.
func TestRenderHeader_hasNoClock(t *testing.T) {
	m := New()
	m.width = 100

	header := m.renderHeader(m.styles())

	assert.Assert(t, !regexp.MustCompile(`\d{2}:\d{2}:\d{2}`).MatchString(header), header)
}

func TestRenderHeader_namesAnUpdate(t *testing.T) {
	m := New().WithConnection(chunkd.Connection{})
	m.width = 100
	next, _ := m.Update(updateCheckMsg{latest: "v9.9.9", upgradeCmd: "chunk upgrade"})
	m = next.(Model)

	header := ansi.Strip(m.renderHeader(m.styles()))
	assert.Check(t, cmp.Contains(header, "↑ v9.9.9 chunk upgrade"))
	assert.Check(t, ansi.StringWidth(strings.TrimSuffix(header, "\n")) <= 100, header)
}

// claimedModel is a dashboard whose flow list has loaded one flow, claimed by
// a session, and its blockers: the list's resume line names the claim. The
// list's preview, which brings the blockers, follows the list, and its poll
// waits 5 seconds, so it runs in a synctest bubble.
func claimedModel(t *testing.T) Model {
	t.Helper()
	flow := circleci.Flow{ID: "b", Title: "Beta", State: "Building - Agent", SessionID: "cc6b734c-1111",
		Next: []circleci.Transition{{Actor: "session", TaskCategory: "build-unit", IsDispatched: true}}}
	list := flowList(t, flow).WithBlockers(func(context.Context, string) ([]planstate.Blocker, error) { return nil, nil })
	m := sized(New().WithConnection(chunkd.Connection{}).WithFlowList(list))
	m = deliverThen(t, m, m.flows.Init(), 2)
	assert.Assert(t, m.flows.ResumeLine() != "", "the resume line did not load")
	return m
}

// The flow list's resume line takes the connection tag's place.
func TestRenderHeader_showsTheResumeLine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := claimedModel(t)

		header := ansi.Strip(m.renderHeader(m.styles()))
		assert.Check(t, cmp.Contains(header, "Claimed by session cc6b734c · factory daemon release b frees it"))
		assert.Check(t, !strings.Contains(header, "✓ local"), header)
		assert.Check(t, cmp.Equal(strings.Count(header, "\n"), 1), header)
		assert.Check(t, ansi.StringWidth(strings.TrimSuffix(header, "\n")) <= m.width, header)
	})
}

// An unreachable daemon outranks the resume line: nothing resumes without it.
func TestRenderHeader_unreachableOutranksTheResumeLine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := claimedModel(t)
		m.daemonErr = errors.New("gone")

		header := ansi.Strip(m.renderHeader(m.styles()))
		assert.Check(t, cmp.Contains(header, "local unreachable"))
		assert.Check(t, !strings.Contains(header, "Claimed by"), header)
	})
}

// With nothing to say about resuming, the header names the daemon.
func TestRenderHeader_noResumeLineShowsTheConnection(t *testing.T) {
	m := sized(New().WithConnection(chunkd.Connection{}).WithFlowList(flowList(t)))
	m = deliverThen(t, m, m.flows.Init(), 1)
	assert.Assert(t, cmp.Equal(m.flows.ResumeLine(), ""))

	header := ansi.Strip(m.renderHeader(m.styles()))
	assert.Check(t, cmp.Contains(header, "✓ local"))
}
