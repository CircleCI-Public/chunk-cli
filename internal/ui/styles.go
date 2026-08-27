package ui

import (
	"os"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

var (
	_hasDark      = lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	_colorEnabled = colorSupported()
	_ld           = lipgloss.LightDark(_hasDark)

	// stdout styles
	styleSuccess  = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("28"), lipgloss.Color("78")))
	styleWarning  = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("136"), lipgloss.Color("179")))
	styleError    = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("160"), lipgloss.Color("167")))
	styleEmphasis = lipgloss.NewStyle().Bold(true)
	styleMuted    = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("240"), lipgloss.Color("242")))
	styleDim      = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("242"), lipgloss.Color("245")))
	styleTeal     = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("30"), lipgloss.Color("80")))

	// stderr styles
	styleErrSuccess  = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("28"), lipgloss.Color("78")))
	styleErrWarning  = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("136"), lipgloss.Color("179")))
	styleErrError    = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("160"), lipgloss.Color("167")))
	styleErrEmphasis = lipgloss.NewStyle().Bold(true)
	styleErrDim      = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("242"), lipgloss.Color("245")))
)

func colorSupported() bool {
	if os.Getenv(config.EnvNoColor) != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// SetColorEnabled overrides automatic color detection for both stdout and stderr.
func SetColorEnabled(enabled bool) {
	_colorEnabled = enabled
}

func render(s lipgloss.Style, text string) string {
	if !_colorEnabled {
		return text
	}
	return s.Render(text)
}

func Red(text string) string    { return render(styleError, text) }
func Green(text string) string  { return render(styleSuccess, text) }
func Yellow(text string) string { return render(styleWarning, text) }
func Cyan(text string) string   { return render(styleTeal, text) }
func Gray(text string) string   { return render(styleMuted, text) }
func Bold(text string) string   { return render(styleEmphasis, text) }
func Dim(text string) string    { return render(styleDim, text) }

func ErrGreen(text string) string  { return render(styleErrSuccess, text) }
func ErrYellow(text string) string { return render(styleErrWarning, text) }
func ErrBold(text string) string   { return render(styleErrEmphasis, text) }
func ErrDim(text string) string    { return render(styleErrDim, text) }
func ErrRed(text string) string    { return render(styleErrError, text) }
