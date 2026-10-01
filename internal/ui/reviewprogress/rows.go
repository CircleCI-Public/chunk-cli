package reviewprogress

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

const (
	maxErrLen     = 50
	sidecarPfxLen = 8
)

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Row is one prompt of a review pass as the row renderers show it. It is the
// same whether the pass runs in this process or on the watch daemon, which is
// what lets `chunk review` and `chunk watch` draw a review identically.
type Row struct {
	Name      string
	SidecarID string
	State     review.PromptState
	Duration  time.Duration
	Err       string
}

// Styles holds the computed lipgloss styles the renderers use.
type Styles struct {
	Dim     lipgloss.Style
	Success lipgloss.Style
	Err     lipgloss.Style
	Running lipgloss.Style
	Muted   lipgloss.Style
	VDim    lipgloss.Style
	Emph    lipgloss.Style
}

// NewStyles computes the styles for a light or dark terminal.
func NewStyles(hasDark bool) Styles {
	ld := lipgloss.LightDark(hasDark)
	return Styles{
		Dim:     lipgloss.NewStyle().Foreground(ld(lipgloss.Color("248"), lipgloss.Color("246"))),
		Success: lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		Err:     lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		Running: lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
		Muted:   lipgloss.NewStyle().Foreground(ld(lipgloss.Color("252"), lipgloss.Color("250"))),
		VDim:    lipgloss.NewStyle().Foreground(ld(lipgloss.Color("244"), lipgloss.Color("242"))),
		Emph:    lipgloss.NewStyle().Bold(true),
	}
}

// NameWidth is the width the name column needs to fit every row.
func NameWidth(rows []Row) int {
	width := 0
	for _, r := range rows {
		if len(r.Name) > width {
			width = len(r.Name)
		}
	}
	return width
}

// RenderRow renders one prompt line, indented two columns. nameWidth pads the
// name column so rows align (see NameWidth); spinIdx picks the spinner frame of
// a running row.
func RenderRow(st Styles, r Row, nameWidth, spinIdx int) string {
	paddedName := fmt.Sprintf("%-*s", nameWidth, r.Name)
	var icon, nameStr, detail string

	switch r.State {
	case review.StateQueued:
		icon = st.VDim.Render("·")
		nameStr = st.VDim.Render(paddedName)
	case review.StateRunning:
		icon = st.Running.Render(spinFrames[spinIdx%len(spinFrames)])
		nameStr = paddedName
		pfx := r.SidecarID
		if len(pfx) > sidecarPfxLen {
			pfx = pfx[:sidecarPfxLen]
		}
		detail = st.Running.Render("↪ " + pfx)
	case review.StateDone:
		icon = st.Success.Render(ui.IconOK)
		nameStr = st.Success.Render(paddedName)
		detail = st.Dim.Render(ui.FormatDuration(r.Duration))
	case review.StateFailed:
		icon = st.Err.Render(ui.IconFail)
		nameStr = st.Err.Render(paddedName)
		// Errors can carry multi-line stderr; keep the row on one line
		// and cut on a rune boundary.
		errTxt := strings.Join(strings.Fields(r.Err), " ")
		if rs := []rune(errTxt); len(rs) > maxErrLen {
			errTxt = string(rs[:maxErrLen-1]) + "…"
		}
		detail = st.Err.Render(errTxt)
	}

	line := "  " + icon + "  " + nameStr
	if detail != "" {
		line += "     " + detail
	}
	return line
}

// RenderSummary renders the "2 running · 1 done" tally for rows.
func RenderSummary(st Styles, rows []Row) string {
	var running, done, queued, failed int
	for _, r := range rows {
		switch r.State {
		case review.StateQueued:
			queued++
		case review.StateRunning:
			running++
		case review.StateDone:
			done++
		case review.StateFailed:
			failed++
		}
	}

	var parts []string
	if running > 0 {
		parts = append(parts, st.Running.Render(fmt.Sprintf("%d running", running)))
	}
	if done > 0 {
		parts = append(parts, st.Success.Render(fmt.Sprintf("%d done", done)))
	}
	if failed > 0 {
		parts = append(parts, st.Err.Render(fmt.Sprintf("%d failed", failed)))
	}
	if queued > 0 {
		parts = append(parts, st.VDim.Render(fmt.Sprintf("%d queued", queued)))
	}
	return strings.Join(parts, st.VDim.Render("  ·  "))
}

// Active reports whether any row is still queued or running.
func Active(rows []Row) bool {
	for _, r := range rows {
		if r.State == review.StateQueued || r.State == review.StateRunning {
			return true
		}
	}
	return false
}
