// Package flowlist is the read-only flow list component. Its host forwards
// messages to it, renders Content, and handles the messages it emits.
package flowlist

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

// OpenMsg asks the host to open a flow's plan workspace. ID is the flow's,
// which is its plan's.
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
	flows []circleci.Flow
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
	list    func(context.Context) ([]circleci.Flow, error)
	preview func(context.Context, string) (string, []circleci.PlanComment, error)
	flows   []circleci.Flow
	// phases are the phases outside phaseOrder, in the order the list
	// first saw them. They are kept across refreshes, so a sub-group does
	// not move when its first flow leaves and comes back.
	phases []string
	id     string
	note   string
	width  int
	height int
	dark   bool
	styles theme.Styles

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

// New creates a list of the flows list returns, each previewed with
// preview. Both callbacks must honor ctx cancellation.
func New(ctx context.Context, list func(context.Context) ([]circleci.Flow, error), preview func(context.Context, string) (string, []circleci.PlanComment, error)) Model {
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

// Len is how many open flows the list shows.
func (m Model) Len() int { return len(m.flows) }

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

// Select selects a flow.
// The next Update starts its preview; hosts can issue Refresh after Select
// to start that update immediately when returning from a workspace.
func (m Model) Select(id string) Model {
	for _, f := range m.flows {
		if f.ID == id {
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
			m.flows = stableFlows(m.flows, msg.flows)
			m.phases = seenPhases(m.phases, m.flows)
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
		flows, err := list(ctx)
		return listedMsg{flows: flows, err: err}
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
		for _, f := range m.visible() {
			if f.ID == m.id {
				return func() tea.Msg { return OpenMsg{ID: f.ID, Title: f.Title} }
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
	flows := m.visible()
	for i, f := range flows {
		if f.ID == m.id {
			m.id = flows[max(0, min(i+step, len(flows)-1))].ID
			return
		}
	}
}

func (m *Model) keepSelection() {
	flows := m.visible()
	for _, f := range flows {
		if f.ID == m.id {
			return
		}
	}
	m.id = ""
	if len(flows) > 0 {
		m.id = flows[0].ID
	}
}

// group is where a flow in state goes in the list. A state chunk does not
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

// phaseOrder is how the phases chunk knows sort within an actor group;
// others follow in the order they first appear.
var phaseOrder = []string{"Planning", "Building"}

// phase is the sub-group a flow in state goes in: the phase part of a
// "Phase - Actor" state, or "" for a state with no actor.
func phase(state string) string {
	p, actor := planstate.Parse(state)
	if actor != planstate.Human && actor != planstate.Agent {
		return ""
	}
	return p
}

// samePhase is whether a and b are one sub-group.
func samePhase(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// seenPhases is known with the phases of flows that are in neither
// phaseOrder nor known appended, in the order flows has them.
func seenPhases(known []string, flows []circleci.Flow) []string {
	for _, f := range flows {
		p := phase(f.State)
		match := func(o string) bool { return samePhase(o, p) }
		if p == "" || slices.ContainsFunc(phaseOrder, match) || slices.ContainsFunc(known, match) {
			continue
		}
		known = append(known, p)
	}
	return known
}

// phaseRank is where phase p sorts within an actor group.
func (m Model) phaseRank(p string) int {
	match := func(o string) bool { return samePhase(o, p) }
	if i := slices.IndexFunc(phaseOrder, match); i >= 0 {
		return i
	}
	if i := slices.IndexFunc(m.phases, match); i >= 0 {
		return len(phaseOrder) + i
	}
	return len(phaseOrder) + len(m.phases)
}

// visible is the flows in the order the list shows them: by actor group,
// then by phase, then in the order the list keeps them.
func (m Model) visible() []circleci.Flow {
	out := slices.Clone(m.flows)
	slices.SortStableFunc(out, func(a, b circleci.Flow) int {
		if c := cmp.Compare(group(a.State), group(b.State)); c != 0 {
			return c
		}
		return cmp.Compare(m.phaseRank(phase(a.State)), m.phaseRank(phase(b.State)))
	})
	return out
}

// Keep surviving rows in their original order, updating their fields in place.
// New rows append within their group; timestamps never move the cursor.
func stableFlows(old, fresh []circleci.Flow) []circleci.Flow {
	byID := make(map[string]circleci.Flow, len(fresh))
	for _, f := range fresh {
		byID[f.ID] = f
	}
	out := make([]circleci.Flow, 0, len(fresh))
	for _, f := range old {
		if next, ok := byID[f.ID]; ok {
			out = append(out, next)
			delete(byID, f.ID)
		}
	}
	for _, f := range fresh {
		if next, ok := byID[f.ID]; ok {
			out = append(out, next)
			delete(byID, f.ID)
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
