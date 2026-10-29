// Package watch implements the chunk watch TUI dashboard: the flow list, and
// the plan screens opened from it.
package watch

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/flowlist"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
	"github.com/CircleCI-Public/chunk-cli/internal/upgrade"
)

// pollInterval is how often the dashboard checks that the daemon answers.
const pollInterval = 5 * time.Second

// watchStyles is the shared TUI palette with the dashboard's shorthand for
// rendering in each style.
type watchStyles struct{ theme.Styles }

func newWatchStyles(hasDark bool) watchStyles { return watchStyles{theme.New(hasDark)} }

func (s watchStyles) emphasis(text string) string { return s.Emphasis.Render(text) }
func (s watchStyles) success(text string) string  { return s.Success.Render(text) }
func (s watchStyles) brand(text string) string    { return s.Brand.Render(text) }
func (s watchStyles) err(text string) string      { return s.Error.Render(text) }
func (s watchStyles) muted(text string) string    { return s.Muted.Render(text) }
func (s watchStyles) dim(text string) string      { return s.Dim.Render(text) }

type tickMsg struct{}
type daemonMsg struct{ err error }
type updateCheckMsg struct{ latest, upgradeCmd string }

// Model is the BubbleTea model for the watch dashboard.
type Model struct {
	width  int
	height int

	hasDarkBG bool // updated by tea.BackgroundColorMsg
	// superEnter is whether the terminal can tell Cmd+Enter from Enter.
	superEnter bool

	updateAvailable string // non-empty tag (e.g. "v1.2.3") when an update is available
	upgradeCmd      string // "chunk upgrade" or "brew upgrade chunk"

	// checkDaemon, when set, is called every pollInterval to check that the
	// daemon answers, and starts it again when it does not. daemonErr is what
	// the last check said, nil when it answered.
	checkDaemon func() error
	daemonErr   error
	// conn is the daemon this dashboard talks to, shown in the header next to
	// whether the last check reached it.
	conn chunkd.Connection

	// note is what the last step on the flow list said.
	note string

	// root is the project the dashboard was opened in, where new plans are
	// made, "" outside one. Each workspace keeps its input and session while
	// another is shown. planScreen points into planWorkspaces, to the last
	// selected workspace.
	root                       string
	planCtx                    context.Context
	planOpening                bool
	planner                    PlanOpener
	builds                     plan.Build
	planScreen                 *plan.Model
	planOpen                   bool
	planWorkspaces             map[uint64]*planWorkspace
	planKey, planSeq           uint64
	flows                      *flowlist.Model
	flowsOpen, confirmPlanQuit bool

	// bottomMargin is how many of the terminal's last rows are left blank, for
	// a terminal that reports more rows than it shows.
	bottomMargin int
}

// New creates a Model ready to run.
func New() Model {
	return Model{
		hasDarkBG:      lipgloss.HasDarkBackground(os.Stdin, os.Stdout),
		conn:           chunkd.CurrentConnection(),
		planWorkspaces: make(map[uint64]*planWorkspace),
	}
}

// WithConnection returns a copy of m that reports conn as the daemon it talks
// to. New reads it from the environment; this is for callers and tests that
// already know.
func (m Model) WithConnection(conn chunkd.Connection) Model {
	m.conn = conn
	return m
}

// WithBottomMargin returns a copy of m that leaves the terminal's last rows
// blank. Editors' built-in terminals can report a row or two more than they
// show, and the rows they hide are the footer's.
func (m Model) WithBottomMargin(rows int) Model {
	m.bottomMargin = max(rows, 0)
	return m
}

// WithRelaunch returns a copy of m that checks the daemon answers, and starts
// it again when a check finds it gone: plans are built on it.
func (m Model) WithRelaunch() Model {
	m.checkDaemon = chunkd.EnsureLaunched
	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{checkUpdateCmd()}
	if m.flows != nil {
		cmds = append(cmds, m.flows.Init())
	}
	if m.checkDaemon != nil {
		cmds = append(cmds, m.pingDaemon)
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m.updateWithPlan(msg)
}

// update handles what is the dashboard's own, rather than the flow list's or
// a plan screen's.
func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.hasDarkBG = msg.IsDark()
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = max(msg.Height-m.bottomMargin, 1)
	case tea.KeyboardEnhancementsMsg:
		m.superEnter = msg.SupportsKeyDisambiguation()
	case updateCheckMsg:
		m.updateAvailable = msg.latest
		m.upgradeCmd = msg.upgradeCmd
	case daemonMsg:
		m.daemonErr = msg.err
		return m, tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })
	case tickMsg:
		return m, m.pingDaemon
	}
	return m, nil
}

// pingDaemon checks the daemon answers.
func (m Model) pingDaemon() tea.Msg {
	if m.checkDaemon == nil {
		return nil
	}
	return daemonMsg{err: m.checkDaemon()}
}

func (m Model) View() tea.View {
	if m.planOpen {
		v := tea.NewView(m.planScreen.Content())
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		v.WindowTitle = "chunk watch"
		return v
	}
	content := ""
	if m.flows != nil {
		content = m.flows.WithNote(m.note).Content()
	}
	if m.width > 0 {
		content = m.renderHeader(m.styles()) + content
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "chunk watch — flows"
	return v
}

func (m Model) styles() watchStyles {
	return newWatchStyles(m.hasDarkBG)
}

func (m Model) renderHeader(st watchStyles) string {
	n := 0
	if m.flows != nil {
		n = m.flows.Len()
	}
	title := st.brand("◆ ") + st.emphasis("chunk watch") + "  " + st.muted(fmt.Sprintf("%d flow%s", n, plural(n)))
	right := m.updateTag(st) + m.connectionTag(st)
	gap := max(m.width-lipgloss.Width(title)-lipgloss.Width(right), 1)
	// Clipped like every other line: one that wraps costs a row the fixed-height
	// layout never budgeted for.
	return clip(title+strings.Repeat(" ", gap)+right, m.width) + "\n"
}

// updateTag names a newer release and the command that installs it.
func (m Model) updateTag(st watchStyles) string {
	if m.updateAvailable == "" {
		return ""
	}
	return "↑ " + m.updateAvailable + " " + st.dim(m.upgradeCmd) + "  "
}

// connectionTag says which daemon the dashboard uses and whether it is
// answering.
func (m Model) connectionTag(st watchStyles) string {
	label := m.conn.Label()
	if m.daemonErr != nil {
		return st.err(ui.IconFail + " " + label + " unreachable")
	}
	return st.success(ui.IconOK) + " " + st.muted(label)
}

// checkUpdateCmd checks for a newer release for the header. Root's
// PersistentPreRunE skips watch (see noUpdateCheckCommands) so this is the
// only check on this path — two would race on the cache file and print the
// notice twice.
func checkUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		latest := upgrade.Check()
		if latest == "" {
			return updateCheckMsg{}
		}
		return updateCheckMsg{latest: latest, upgradeCmd: upgrade.SelfUpgradeCommand()}
	}
}

// clip cuts a line to at most width display columns, measuring with lipgloss so
// ANSI sequences are not counted as visible width and are not cut mid-escape.
func clip(s string, width int) string {
	if width < 1 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
