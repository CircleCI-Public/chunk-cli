// Package flowlist is the read-only flow list component. Its host forwards
// messages to it, renders Content, and handles the messages it emits.
package flowlist

import (
	"cmp"
	"context"
	"maps"
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

// QuitMsg asks the host to quit after checking for unsent workspace input.
type QuitMsg struct{}

type refreshMsg struct{}
type pollMsg struct{ generation uint64 }
type listedMsg struct {
	flows      []circleci.Flow
	err        error
	parents    map[string]string
	parentsErr error
}
type previewedMsg struct {
	id         string
	generation uint64
	text       string
	comments   []circleci.PlanComment
	err        error
	// lines is text rendered for key, when the fetch rendered it.
	lines []string
	key   renderKey
}

// renderedMsg is a flow's cached text rendered for key, off the UI goroutine.
type renderedMsg struct {
	id    string
	key   renderKey
	lines []string
}

// Model is a Bubble Tea component, not a standalone tea.Model.
type Model struct {
	ctx     context.Context
	list    func(context.Context) ([]circleci.Flow, error)
	preview func(context.Context, string) (string, []circleci.PlanComment, error)
	parents func(context.Context) (map[string]string, error)
	flows   []circleci.Flow
	// parentOf is each sub-plan's parent plan ID, from the last list.
	parentOf   map[string]string
	parentsErr error
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
	previewErr        error
	// cache is each flow's last preview and its render, by flow ID, so
	// selecting a flow again shows it while a fresh one loads. Only Update
	// writes it, on the Bubble Tea goroutine.
	cache map[string]cached
	// render is glamour unless a test replaces it.
	render func(text string, width int, dark bool) []string
}

// cached is a flow's last preview, and its text as rendered for key.
type cached struct {
	text     string
	comments []circleci.PlanComment
	lines    []string
	key      renderKey
	// pending is the key of a render in flight, so one is not started twice.
	pending renderKey
}

// renderKey is what a rendered preview depends on.
type renderKey struct {
	text  string
	width int
	dark  bool
}

// New creates a list of the flows list returns, each previewed with
// preview. Both callbacks must honor ctx cancellation.
func New(ctx context.Context, list func(context.Context) ([]circleci.Flow, error), preview func(context.Context, string) (string, []circleci.PlanComment, error)) Model {
	return Model{
		ctx: ctx, list: list, preview: preview, width: 80, height: 24, dark: true, styles: theme.New(true),
		cache: map[string]cached{}, render: glamourLines,
	}
}

// WithParents has the list read each flow's parent plan with parents, a
// map from plan ID to its parent's ID, after each successful list. A flow
// whose parent is listed is drawn as its sub-plan.
func (m Model) WithParents(parents func(context.Context) (map[string]string, error)) Model {
	m.parents = parents
	return m
}

// Init requests the first list. Polling starts when that request finishes.
func (m Model) Init() tea.Cmd { return m.Refresh() }

// WithNote sets the host's note, a line under the help bar. An empty note
// clears it.
func (m Model) WithNote(note string) Model {
	m.note = oneLine(note)
	return m
}

// Flows is the open flows as the list last read them.
func (m Model) Flows() []circleci.Flow { return slices.Clone(m.flows) }

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
			m.parentOf, m.parentsErr = msg.parents, msg.parentsErr
			if msg.parentsErr != nil {
				m.parentOf = nil
			}
			m.loaded = true
			maps.DeleteFunc(m.cache, func(id string, _ cached) bool {
				return !slices.ContainsFunc(m.flows, func(f circleci.Flow) bool { return f.ID == id })
			})
			m.keepSelection()
			refreshPreview = true
		}
		cmds = append(cmds, m.poll())
	case previewedMsg:
		if msg.id == m.id && msg.generation == m.previewGeneration {
			m.previewing = false
			m.previewErr = msg.err
			if msg.err == nil {
				// Lines and key stay unless the fetch rendered the text for
				// the pane as it is now, so equal text does not render again
				// and a render for an old size or style is dropped.
				entry := m.cache[msg.id]
				entry.text, entry.comments = msg.text, msg.comments
				if msg.lines != nil && msg.key == m.renderKey(msg.text) {
					entry.lines, entry.key = msg.lines, msg.key
				}
				m.cache[msg.id] = entry
				m.previewLoaded = true
			}
		}
	case renderedMsg:
		if entry, ok := m.cache[msg.id]; ok {
			if entry.pending == msg.key {
				entry.pending = renderKey{}
			}
			if msg.key == m.renderKey(entry.text) {
				entry.lines, entry.key = msg.lines, msg.key
			}
			m.cache[msg.id] = entry
		}
	case tea.KeyPressMsg:
		cmds = append(cmds, m.key(msg))
	}
	if m.previewID != m.id {
		m.previewID = m.id
		m.previewGeneration++
		m.previewing, m.previewErr = false, nil
		// A flow previewed before shows that preview while a fresh one loads.
		_, m.previewLoaded = m.cache[m.id]
		refreshPreview = true
	}
	// Before startPreview, so the fetch knows a render is already in flight.
	cmds = append(cmds, m.renderPreview())
	if m.id != "" && !m.previewing && (refreshPreview || !m.previewLoaded && m.previewErr == nil) {
		cmds = append(cmds, m.startPreview())
	}
	return m, tea.Batch(cmds...)
}

// renderKey is the key text rendered for the preview pane as it is now
// would have: the zero key when the pane is not drawn or text is blank.
func (m Model) renderKey(text string) renderKey {
	w := m.previewWidth()
	if w == 0 || strings.TrimSpace(text) == "" {
		return renderKey{}
	}
	return renderKey{text: text, width: w - 4, dark: m.dark}
}

// renderPreview starts a render of the selected flow's cached plan text,
// unless it was rendered, or is being rendered, for the same text, width and
// style. One render of a large plan takes longer than a frame, so it runs
// off the UI goroutine; the cached lines show until it returns.
func (m *Model) renderPreview() tea.Cmd {
	entry, ok := m.cache[m.id]
	if m.id == "" || !ok {
		return nil
	}
	key := m.renderKey(entry.text)
	if key == (renderKey{}) || entry.key == key || entry.pending == key {
		return nil
	}
	entry.pending = key
	m.cache[m.id] = entry
	ctx, render, id := m.ctx, m.render, m.id
	return func() tea.Msg {
		if ctx.Err() != nil {
			return nil
		}
		return renderedMsg{id: id, key: key, lines: render(key.text, key.width, key.dark)}
	}
}

func (m *Model) startList() tea.Cmd {
	if m.listing {
		return nil
	}
	m.listing = true
	m.pollGeneration++ // Invalidates an earlier timer after a host refresh.
	ctx, list, parents := m.ctx, m.list, m.parents
	return func() tea.Msg {
		if ctx.Err() != nil {
			return nil
		}
		flows, err := list(ctx)
		msg := listedMsg{flows: flows, err: err}
		if err == nil && parents != nil {
			msg.parents, msg.parentsErr = parents(ctx)
		}
		return msg
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
	ctx, preview, render, id, generation := m.ctx, m.preview, m.render, m.id, m.previewGeneration
	// The fetch renders its text for the pane as it is now, off the UI
	// goroutine, unless that render is cached or in flight already.
	entry := m.cache[id]
	have := entry.key
	if entry.pending != (renderKey{}) {
		have = entry.pending
	}
	width, dark, drawn := m.previewWidth()-4, m.dark, m.previewWidth() > 0
	return func() tea.Msg {
		if ctx.Err() != nil {
			return nil
		}
		text, comments, err := preview(ctx, id)
		msg := previewedMsg{id: id, generation: generation, text: text, comments: comments, err: err}
		key := renderKey{text: text, width: width, dark: dark}
		if err == nil && drawn && strings.TrimSpace(text) != "" && key != have {
			msg.lines, msg.key = render(text, width, dark), key
		}
		return msg
	}
}

func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c", "q", "esc":
		return func() tea.Msg { return QuitMsg{} }
	case "enter":
		flows, _ := m.visible()
		for _, f := range flows {
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
	}
	return nil
}

func (m *Model) move(step int) {
	flows, _ := m.visible()
	for i, f := range flows {
		if f.ID == m.id {
			m.id = flows[max(0, min(i+step, len(flows)-1))].ID
			return
		}
	}
}

func (m *Model) keepSelection() {
	flows, _ := m.visible()
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

// group is where flow f goes in the list: by who its next waits on, after
// Not started. A flow whose next is empty, such as one its blockers hold,
// goes in the last group.
func group(f circleci.Flow) int {
	switch {
	case planstate.IsUnprioritized(f.State):
		return 2
	case waitsOnHuman(f.Next):
		return 0
	case agentActs(f.Next):
		return 1
	}
	return 3
}

// waitsOnHuman is whether next waits only on the developer: a transition
// for them other than the escape hatches, and none by a session or the
// daemon. merge-pr, an action, counts.
func waitsOnHuman(next []circleci.Transition) bool {
	return !agentActs(next) && slices.ContainsFunc(next, func(t circleci.Transition) bool {
		e := t.Event
		return t.Actor == "human" && e != circleci.Abandon && e != "abandon-integrate"
	})
}

// agentActs is whether next has a transition a session or the daemon
// takes.
func agentActs(next []circleci.Transition) bool {
	return slices.ContainsFunc(next, func(t circleci.Transition) bool {
		return t.Actor == "session" || t.Actor == "daemon"
	})
}

// phaseOrder is how the phases chunk knows sort within a group;
// others follow in the order they first appear.
var phaseOrder = []string{"Planning", "Building"}

// phase is the sub-group flow f goes in within the first two groups: its
// state's phase, or "" in the others.
func phase(f circleci.Flow) string {
	if g := group(f); g != 0 && g != 1 {
		return ""
	}
	p, _ := planstate.Parse(f.State)
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
		p := phase(f)
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

// visible is the flows in the order the list shows them, and how deep each
// is drawn under its parent: by actor group, then by phase, then with each
// sub-plan after a parent in its sub-group, then in the order the list
// keeps them.
func (m Model) visible() ([]circleci.Flow, map[string]int) {
	sorted := slices.Clone(m.flows)
	slices.SortStableFunc(sorted, func(a, b circleci.Flow) int {
		if c := cmp.Compare(group(a), group(b)); c != 0 {
			return c
		}
		return cmp.Compare(m.phaseRank(phase(a)), m.phaseRank(phase(b)))
	})
	out := make([]circleci.Flow, 0, len(sorted))
	depth := make(map[string]int)
	for len(sorted) > 0 {
		n := 1
		for n < len(sorted) && m.sameSubGroup(sorted[0], sorted[n]) {
			n++
		}
		out = m.nest(out, sorted[:n], depth)
		sorted = sorted[n:]
	}
	return out, depth
}

// sameSubGroup is whether a and b are listed under one heading.
func (m Model) sameSubGroup(a, b circleci.Flow) bool {
	return group(a) == group(b) && m.phaseRank(phase(a)) == m.phaseRank(phase(b))
}

// nest appends run, the flows of one sub-group, to out depth-first: each
// flow whose parent is not in run, in order, then its sub-plans in run,
// recursively. It records the depth of each one below the first level.
func (m Model) nest(out, run []circleci.Flow, depth map[string]int) []circleci.Flow {
	in := make(map[string]bool, len(run))
	for _, f := range run {
		in[f.ID] = true
	}
	children := make(map[string][]circleci.Flow)
	var roots []circleci.Flow
	for _, f := range run {
		if p := m.parentOf[f.ID]; p != f.ID && in[p] {
			children[p] = append(children[p], f)
			continue
		}
		roots = append(roots, f)
	}
	visited := make(map[string]bool, len(run))
	var walk func(f circleci.Flow, d int)
	walk = func(f circleci.Flow, d int) {
		if visited[f.ID] {
			return
		}
		visited[f.ID] = true
		out = append(out, f)
		if d > 0 {
			depth[f.ID] = d
		}
		for _, c := range children[f.ID] {
			walk(c, d+1)
		}
	}
	for _, f := range roots {
		walk(f, 0)
	}
	// Flows whose parents form a cycle have no root; they follow in order.
	for _, f := range run {
		walk(f, 0)
	}
	return out
}

// parentTitle is the title of id's parent when the parent is listed, else "".
func (m Model) parentTitle(id string) string {
	p, ok := m.parentOf[id]
	if !ok || p == id {
		return ""
	}
	for _, f := range m.flows {
		if f.ID != p {
			continue
		}
		if t := oneLine(f.Title); t != "" {
			return t
		}
		return "Untitled plan"
	}
	return ""
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
