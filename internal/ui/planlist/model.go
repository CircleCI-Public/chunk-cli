// Package planlist is the read-only plan inbox component. Its host forwards
// messages to it, renders Content, and handles the messages it emits.
package planlist

import (
	"context"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

// OpenMsg asks the host to open an existing plan workspace.
type OpenMsg struct{ ID, Title string }

// NewMsg asks the host to start a new plan workspace.
type NewMsg struct{}

// BackMsg asks the host to show runs, without ending the program.
type BackMsg struct{}

// QuitMsg asks the host to quit after checking for unsent workspace input.
type QuitMsg struct{}

type refreshMsg struct{}
type pollMsg struct{ generation uint64 }
type listedMsg struct {
	plans []circleci.Plan
	err   error
}
type previewedMsg struct {
	id         string
	generation uint64
	text       string
	comments   []circleci.PlanComment
	err        error
}

// Model is a Bubble Tea component, not a standalone tea.Model.
type Model struct {
	ctx     context.Context
	list    func(context.Context) ([]circleci.Plan, error)
	preview func(context.Context, string) (string, []circleci.PlanComment, error)
	plans   []circleci.Plan
	id      string
	note    string
	width   int
	height  int
	dark    bool
	styles  theme.Styles

	loaded, listing bool
	listErr         error
	pollGeneration  uint64

	previewID         string
	previewGeneration uint64
	previewing        bool
	previewLoaded     bool
	text              string
	comments          []circleci.PlanComment
	previewErr        error
}

// New creates an inbox of the plans list returns, each previewed with
// preview. Both callbacks must honor ctx cancellation.
func New(ctx context.Context, list func(context.Context) ([]circleci.Plan, error), preview func(context.Context, string) (string, []circleci.PlanComment, error)) Model {
	return Model{ctx: ctx, list: list, preview: preview, width: 80, height: 24, dark: true, styles: theme.New(true)}
}

// Init requests the first list. Polling starts when that request finishes.
func (m Model) Init() tea.Cmd { return m.Refresh() }

// WithNote sets the host's note, a line under the help bar. An empty note
// clears it.
func (m Model) WithNote(note string) Model {
	m.note = oneLine(note)
	return m
}

// Len is how many open plans the inbox lists.
func (m Model) Len() int { return len(m.plans) }

// Refresh requests fresh list and preview data. Concurrent refresh requests
// coalesce; only one list callback can be in flight at a time.
func (m Model) Refresh() tea.Cmd {
	return func() tea.Msg {
		if m.ctx.Err() != nil {
			return nil
		}
		return refreshMsg{}
	}
}

// Select selects an existing plan.
// The next Update starts its preview; hosts can issue Refresh after Select
// to start that update immediately when returning from a workspace.
func (m Model) Select(id string) Model {
	for _, p := range m.plans {
		if p.ID == id {
			m.id = id
			return m
		}
	}
	return m
}

// Update advances the component. Navigation and quit requests are commands;
// the host decides whether it is safe to leave a workspace or end the program.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if m.ctx.Err() != nil {
		return m, nil
	}
	var cmds []tea.Cmd
	refreshPreview := false
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 0), max(msg.Height, 0)
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.styles = theme.New(m.dark)
	case refreshMsg:
		cmds = append(cmds, m.startList())
	case pollMsg:
		if msg.generation == m.pollGeneration {
			cmds = append(cmds, m.startList())
		}
	case listedMsg:
		m.listing = false
		m.listErr = msg.err
		if msg.err == nil {
			m.plans = stablePlans(m.plans, msg.plans)
			m.loaded = true
			m.keepSelection()
			refreshPreview = true
		}
		cmds = append(cmds, m.poll())
	case previewedMsg:
		if msg.id == m.id && msg.generation == m.previewGeneration {
			m.previewing = false
			m.previewErr = msg.err
			if msg.err == nil {
				m.text, m.comments, m.previewLoaded = msg.text, msg.comments, true
			}
		}
	case tea.KeyPressMsg:
		cmds = append(cmds, m.key(msg))
	}
	if m.previewID != m.id {
		m.previewID = m.id
		m.previewGeneration++
		m.previewing, m.previewLoaded = false, false
		m.text, m.comments, m.previewErr = "", nil, nil
	}
	if m.id != "" && !m.previewing && (refreshPreview || !m.previewLoaded && m.previewErr == nil) {
		cmds = append(cmds, m.startPreview())
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) startList() tea.Cmd {
	if m.listing {
		return nil
	}
	m.listing = true
	m.pollGeneration++ // Invalidates an earlier timer after a host refresh.
	ctx, list := m.ctx, m.list
	return func() tea.Msg {
		if ctx.Err() != nil {
			return nil
		}
		plans, err := list(ctx)
		return listedMsg{plans: plans, err: err}
	}
}

func (m Model) poll() tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-m.ctx.Done():
			return nil
		case <-timer.C:
			return pollMsg{generation: m.pollGeneration}
		}
	}
}

func (m *Model) startPreview() tea.Cmd {
	m.previewing = true
	m.previewGeneration++
	ctx, preview, id, generation := m.ctx, m.preview, m.id, m.previewGeneration
	return func() tea.Msg {
		if ctx.Err() != nil {
			return nil
		}
		text, comments, err := preview(ctx, id)
		return previewedMsg{id: id, generation: generation, text: text, comments: comments, err: err}
	}
}

func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		return func() tea.Msg { return QuitMsg{} }
	case "tab", "esc", "2":
		return func() tea.Msg { return BackMsg{} }
	case "enter":
		for _, p := range m.visible() {
			if p.ID == m.id {
				return func() tea.Msg { return OpenMsg{ID: p.ID, Title: p.Title} }
			}
		}
		return nil
	case "down", "j":
		m.move(1)
	case "up", "k":
		m.move(-1)
	case "n":
		return func() tea.Msg { return NewMsg{} }
	case "q":
		return func() tea.Msg { return QuitMsg{} }
	}
	return nil
}

func (m *Model) move(step int) {
	plans := m.visible()
	for i, p := range plans {
		if p.ID == m.id {
			m.id = plans[max(0, min(i+step, len(plans)-1))].ID
			return
		}
	}
}

func (m *Model) keepSelection() {
	plans := m.visible()
	for _, p := range plans {
		if p.ID == m.id {
			return
		}
	}
	m.id = ""
	if len(plans) > 0 {
		m.id = plans[0].ID
	}
}

// group is where a plan in state goes in the inbox. A state chunk does not
// know goes in the last group.
func group(state string) int {
	if planstate.IsUnprioritized(state) {
		return 2
	}
	switch _, actor := planstate.Parse(state); actor {
	case planstate.Human:
		return 0
	case planstate.Agent:
		return 1
	case planstate.Unknown:
	}
	return 3
}

func (m Model) visible() []circleci.Plan {
	var out []circleci.Plan
	for g := 0; g < 4; g++ {
		for _, p := range m.plans {
			if group(p.State) == g {
				out = append(out, p)
			}
		}
	}
	return out
}

// Keep surviving rows in their original order, updating their fields in place.
// New rows append within their group; timestamps never move the cursor.
func stablePlans(old, fresh []circleci.Plan) []circleci.Plan {
	byID := make(map[string]circleci.Plan, len(fresh))
	for _, p := range fresh {
		byID[p.ID] = p
	}
	out := make([]circleci.Plan, 0, len(fresh))
	for _, p := range old {
		if next, ok := byID[p.ID]; ok {
			out = append(out, next)
			delete(byID, p.ID)
		}
	}
	for _, p := range fresh {
		if next, ok := byID[p.ID]; ok {
			out = append(out, next)
			delete(byID, p.ID)
		}
	}
	return out
}

// Strip terminal escapes and controls from server text and pasted input.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' || r == '\r' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}
