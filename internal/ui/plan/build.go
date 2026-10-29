package plan

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// Build reads what the chunk daemon's build session of a plan has done.
type Build interface {
	// Activity is plan planID's build and its lines past the first after.
	// Found is false when there is no build of the plan to read.
	Activity(ctx context.Context, planID string, after int) (BuildActivity, error)
}

// BuildActivity is a plan's build session and the lines of its feed past
// those the screen has read.
type BuildActivity struct {
	// Found is whether there is a build of the plan; nothing else is set
	// when there is not.
	Found bool
	// ID is the build session's; a new ID starts a new section.
	ID      string
	Running bool
	// Failed is why the session failed, "" when it did not.
	Failed string
	// First is the number of Lines[0] in the session's feed.
	First int
	Lines []BuildLine
	// Total is how many lines the session has written.
	Total int
}

// BuildLine is one line of a build session's feed.
type BuildLine struct {
	Level BuildLevel
	Text  string
}

// BuildLevel is how much a build line matters, which decides its style.
type BuildLevel int

const (
	// BuildInfo is the session's activity: a tool call or piece of text.
	BuildInfo BuildLevel = iota
	// BuildStep is a reply or a note of the build's progress.
	BuildStep
	// BuildWarn is something the developer may need to act on.
	BuildWarn
	// BuildError is something that went wrong.
	BuildError
)

// WithBuild returns a copy of m that reads its plan's build from b, and shows
// it in the Activity pane while the plan is being built.
func (m Model) WithBuild(b Build) Model {
	m.build = b
	return m
}

// buildEvery is how often the screen reads its plan's build while one is
// expected.
const buildEvery = 2 * time.Second

type (
	buildTickMsg struct{}
	// buildReadMsg is the answer to one read of the plan's build.
	buildReadMsg struct {
		// after is what the read asked for, so a new session read past its
		// start is read again from 0.
		after    int
		activity BuildActivity
		err      error
	}
)

func buildTickCmd() tea.Cmd {
	return tea.Tick(buildEvery, func(time.Time) tea.Msg { return buildTickMsg{} })
}

func readBuildCmd(ctx context.Context, b Build, planID string, after int) tea.Cmd {
	return func() tea.Msg {
		a, err := b.Activity(ctx, planID, after)
		return buildReadMsg{after: after, activity: a, err: err}
	}
}

// maxBuildRows is how many of a build's lines the section keeps.
const maxBuildRows = 100

// buildRun is one build session as the screen has read it.
type buildRun struct {
	id string
	// first is the number of rows[0] among every line the session wrote.
	first int
	rows  []BuildLine
	// total is how many lines the session has written: the next read's after.
	total   int
	running bool
	failed  string
}

// expectsBuild is whether the plan has a build to follow.
func (m Model) expectsBuild() bool {
	if m.build == nil || m.store.ID() == "" || m.flow.Ended {
		return false
	}
	phase, _ := planstate.Parse(m.state)
	return m.built || strings.EqualFold(phase, "Building") || strings.EqualFold(phase, "Integrating")
}

// followBuild starts reading the build when one is expected and no read is
// under way.
func (m Model) followBuild() (Model, tea.Cmd) {
	if m.reading || !m.expectsBuild() {
		return m, nil
	}
	m.reading = true
	return m, readBuildCmd(m.ctx, m.build, m.store.ID(), m.buildAfter())
}

// updateBuild handles the build's tick and the answers to its reads. reading
// stays true from a read until its tick fires, so a refresh in between starts
// no second loop.
func (m Model) updateBuild(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case buildTickMsg:
		m.reading = false
		return m.followBuild()
	case buildReadMsg:
		wasSpinning := m.spinning()
		var again bool
		m, again = m.applyBuildRead(msg)
		if again {
			// reading stays true: this read takes the place of the tick.
			return m, readBuildCmd(m.ctx, m.build, m.store.ID(), 0)
		}
		if !m.expectsBuild() {
			m.reading = false
			return m, nil
		}
		var spin tea.Cmd
		if !wasSpinning && m.spinning() {
			spin = m.spin.Tick
		}
		return m, tea.Batch(buildTickCmd(), spin)
	}
	return m, nil
}

// buildAfter is the after of the next read: the latest session's total.
func (m Model) buildAfter() int {
	if len(m.builds) == 0 {
		return 0
	}
	return m.builds[len(m.builds)-1].total
}

// buildRunning is whether the latest build session read is still running.
func (m Model) buildRunning() bool {
	return len(m.builds) > 0 && m.builds[len(m.builds)-1].running
}

// applyBuildRead folds an answer to a read of the build in, and reports
// whether the build must be read again at once from 0: the answer is of a
// new session, read past its start.
func (m Model) applyBuildRead(msg buildReadMsg) (Model, bool) {
	if msg.err != nil {
		m.buildErr = msg.err.Error()
		m.renderChat()
		return m, false
	}
	m.buildErr = ""
	a := msg.activity
	m.buildMissing = !a.Found
	if !a.Found {
		m.renderChat()
		return m, false
	}
	newRun := len(m.builds) == 0 || m.builds[len(m.builds)-1].id != a.ID
	if newRun && msg.after > 0 {
		// This answer may have skipped the new session's first lines.
		m.builds = append(m.builds, buildRun{id: a.ID})
		m.renderChat()
		return m, true
	}
	if newRun {
		m.builds = append(m.builds, buildRun{id: a.ID, first: a.First})
	}
	run := &m.builds[len(m.builds)-1]
	if a.First > run.first+len(run.rows) {
		// Lines were dropped between reads; the rows kept stay contiguous.
		run.rows, run.first = nil, a.First
	}
	run.rows = append(run.rows, a.Lines...)
	if over := len(run.rows) - maxBuildRows; over > 0 {
		run.rows = append([]BuildLine(nil), run.rows[over:]...)
		run.first += over
	}
	run.total, run.running, run.failed = a.Total, a.Running, a.Failed
	m.renderChat()
	return m, false
}

// showsBuild is whether the Activity pane has a Building section.
func (m Model) showsBuild() bool {
	return len(m.builds) > 0 || m.buildMissing || m.buildErr != ""
}

// buildSection is the build's feed, under a divider per session.
func (m Model) buildSection(w int) string {
	var b strings.Builder
	divider := func() {
		b.WriteString("\n\n" + m.style(m.faint()).Render(ansi.Truncate("── Building "+strings.Repeat("─", w), w, "")) + "\n")
	}
	for i, run := range m.builds {
		divider()
		if run.first > 0 {
			b.WriteString(m.style(m.faint()).Render(fmt.Sprintf("… %d earlier lines not kept", run.first)) + "\n")
		}
		last := i == len(m.builds)-1
		for j, l := range run.rows {
			text := ansi.Truncate(strings.Join(strings.Fields(l.Text), " "), w-2, "…")
			if last && run.running && j == len(run.rows)-1 {
				b.WriteString(m.style(m.purple()).Render(m.spin.View()+text) + "\n")
				continue
			}
			b.WriteString(m.buildRowStyle(l.Level).Render(text) + "\n")
		}
		switch {
		case run.failed != "":
			b.WriteString(ui.ErrorStyle.Render(ansi.Truncate("Build session failed: "+run.failed, w, "…")) + "\n")
		case !run.running:
			b.WriteString(m.style(m.dim()).Render("Build session ended") + "\n")
		}
	}
	if len(m.builds) == 0 {
		divider()
	}
	if m.buildMissing {
		b.WriteString(m.style(m.dim()).Render(ansi.Truncate("No build of this plan in this machine's chunk daemon", w, "…")) + "\n")
	}
	if m.buildErr != "" {
		b.WriteString(m.style(m.dim()).Render(ansi.Truncate("Could not read the build: "+m.buildErr, w, "…")) + "\n")
	}
	return b.String()
}

func (m Model) buildRowStyle(l BuildLevel) lipgloss.Style {
	switch l {
	case BuildWarn:
		return ui.WarningStyle
	case BuildError:
		return ui.ErrorStyle
	case BuildStep:
		return lipgloss.NewStyle()
	case BuildInfo:
	}
	return m.style(m.dim())
}
