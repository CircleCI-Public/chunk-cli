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

// Build reads what the chunk daemon's build session of a plan has done, and
// stops it.
type Build interface {
	// Activity is plan planID's build and its lines past the first after.
	// Found is false when there is no build of the plan to read.
	Activity(ctx context.Context, planID string, after int) (BuildActivity, error)
	// Stop stops build session sessionID. Stopping one that has already ended
	// is not an error.
	Stop(ctx context.Context, sessionID string) error
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
	// Cancelled is whether the session was stopped.
	Cancelled bool
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
// it in the Overview's Building stage while the plan is being built.
func (m Model) WithBuild(b Build) Model {
	m.build = b
	return m
}

// Resumer starts a plan's agent again in the chunk daemon, in the project at
// root, as kind says.
type Resumer interface {
	Resume(ctx context.Context, root, planID string, kind planstate.ResumeKind, holder string) error
}

// WithResume returns a copy of m whose R starts a build session of its plan
// through r, when factory waits on one that is not there. Without it, R only
// resumes the plan's planner.
func (m Model) WithResume(r Resumer) Model {
	m.resumer = r
	return m
}

// buildResumedMsg is the end of starting a build session of the plan.
type buildResumedMsg struct{ err error }

func resumeBuildCmd(ctx context.Context, r Resumer, root, planID string) tea.Cmd {
	return func() tea.Msg { return buildResumedMsg{err: r.Resume(ctx, root, planID, planstate.ResumeBuild, "")} }
}

// takeOverDoneMsg is the end of releasing the plan's holder and resuming its
// planner.
type takeOverDoneMsg struct{ err error }

func takeOverCmd(ctx context.Context, r Resumer, root, planID, holder string) tea.Cmd {
	return func() tea.Msg {
		return takeOverDoneMsg{err: r.Resume(ctx, root, planID, planstate.ResumeTakeOver, holder)}
	}
}

// buildStoppedMsg is the end of stopping the plan's build session.
type buildStoppedMsg struct{ err error }

func stopBuildCmd(ctx context.Context, b Build, sessionID string) tea.Cmd {
	return func() tea.Msg { return buildStoppedMsg{err: b.Stop(ctx, sessionID)} }
}

// latestBuildID is the latest build session read, "" when none has been.
func (m Model) latestBuildID() string {
	if len(m.builds) == 0 {
		return ""
	}
	return m.builds[len(m.builds)-1].id
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

func (m Model) buildTickCmd() tea.Cmd {
	return m.after(buildEvery, func(time.Time) tea.Msg { return buildTickMsg{} })
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
	// stage is the stage stageNow gave when the session was first read:
	// Building once the screen has approved or started a build of the plan,
	// even while factory has yet to move it.
	stage string
	// seq is the session's place among the log's entries, as the messages'
	// are numbered.
	seq int
	// first is the number of rows[0] among every line the session wrote.
	first int
	rows  []BuildLine
	// total is how many lines the session has written: the next read's after.
	total     int
	running   bool
	cancelled bool
	failed    string
}

// expectsBuild is whether the plan has a build to follow.
func (m Model) expectsBuild() bool {
	if m.build == nil || m.store.ID() == "" || m.flow.Ended {
		return false
	}
	state := clean(m.state)
	return m.built || state == planstate.Build || state == planstate.Integrate
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

// updateBuild handles the build's tick, the answers to its reads and the end
// of a stop. reading stays true from a read until its tick fires, so a
// refresh in between starts no second loop.
func (m Model) updateBuild(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case buildStoppedMsg:
		m.stoppingBuild = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render("Could not stop the build session: " + msg.err.Error())
			return m, nil
		}
		// The read loop picks up the stop; the refresh shows the freed claim.
		m.status = ""
		return m, refreshCmd(m.ctx, m.store)
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
		return m, tea.Batch(m.buildTickCmd(), spin)
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
		m.renderLeft()
		return m, false
	}
	m.buildErr = ""
	a := msg.activity
	m.buildMissing = !a.Found
	if !a.Found {
		m.renderLeft()
		return m, false
	}
	newRun := len(m.builds) == 0 || m.builds[len(m.builds)-1].id != a.ID
	if newRun && msg.after > 0 {
		// This answer may have skipped the new session's first lines.
		m.seq++
		m.builds = append(m.builds, buildRun{id: a.ID, stage: m.stageNow(), seq: m.seq})
		m.renderLeft()
		return m, true
	}
	if newRun {
		m.seq++
		m.builds = append(m.builds, buildRun{id: a.ID, stage: m.stageNow(), seq: m.seq, first: a.First})
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
	run.total, run.running, run.cancelled, run.failed = a.Total, a.Running, a.Cancelled, a.Failed
	m.renderLeft()
	return m, false
}

// buildSection draws runs, build sessions, newest first, w cells wide. With
// notes, what the reads of the build said of it is above the sessions.
func (m Model) buildSection(runs []buildRun, notes bool, w int) string {
	var b strings.Builder
	divider := func() {
		b.WriteString(m.style(m.faint()).Render(ansi.Truncate("── build session "+strings.Repeat("─", w), w, "")) + "\n")
	}
	if notes {
		if n := m.buildNotes(w); len(n) > 0 {
			if len(runs) == 0 {
				divider()
			}
			b.WriteString(strings.Join(n, "\n") + "\n")
		}
	}
	latest := m.latestBuildID()
	for i := range runs {
		run := runs[len(runs)-1-i]
		if i > 0 {
			b.WriteString("\n")
		}
		divider()
		switch {
		case run.failed != "":
			b.WriteString(ui.ErrorStyle.Render(ansi.Truncate("Build session failed: "+run.failed, w, "…")) + "\n")
		case run.cancelled:
			b.WriteString(m.style(m.dim()).Render("Build session stopped") + "\n")
		case !run.running:
			b.WriteString(m.style(m.dim()).Render("Build session ended") + "\n")
		}
		last := run.id == latest
		for j := len(run.rows) - 1; j >= 0; j-- {
			text := ansi.Truncate(strings.Join(strings.Fields(run.rows[j].Text), " "), w-2, "…")
			if last && run.running && j == len(run.rows)-1 {
				b.WriteString(m.style(m.purple()).Render(m.spin.View()+text) + "\n")
				continue
			}
			b.WriteString(m.buildRowStyle(run.rows[j].Level).Render(text) + "\n")
		}
		if run.first > 0 {
			b.WriteString(m.style(m.faint()).Render(fmt.Sprintf("… %d earlier lines not kept", run.first)) + "\n")
		}
	}
	return b.String()
}

// buildNotes is what the reads of the build said of it, as lines w cells wide:
// that there is none, or that it could not be read.
func (m Model) buildNotes(w int) []string {
	var out []string
	if m.buildErr != "" {
		out = append(out, m.style(m.dim()).Render(ansi.Truncate("Could not read the build: "+m.buildErr, w, "…")))
	}
	if m.buildMissing {
		out = append(out, m.style(m.dim()).Render(ansi.Truncate("No build of this plan in this machine's chunk daemon", w, "…")))
	}
	return out
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
