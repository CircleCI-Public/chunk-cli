package ui

import (
	"os"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// Status icons. Keep these in one place so prompts, results, and plain-text
// fallbacks all use the same glyph.
const (
	IconOK   = "✓"
	IconWarn = "⚠"
	IconFail = "✗"
)

var (
	_hasDark      = lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	_colorEnabled = colorSupported()
	_ld           = lipgloss.LightDark(_hasDark)

	// SuccessStyle is for completed actions and passing status glyphs.
	SuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	// WarningStyle is for warnings and cautionary prompts.
	WarningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	// ErrorStyle is for failures and error glyphs.
	ErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	// TitleStyle is for prompt headings and emphasized labels.
	TitleStyle = lipgloss.NewStyle().Bold(true)
	// HelperStyle is for de-emphasized hint text and secondary labels.
	HelperStyle = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("252"), lipgloss.Color("250")))
	// NoColorStyle is the fallback when color output is disabled.
	NoColorStyle = lipgloss.NewStyle().Foreground(lipgloss.NoColor{})

	// Internal styles without a direct semantic equivalent.
	styleDim  = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("248"), lipgloss.Color("246")))
	styleTeal = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))

	// Stderr variants share the same colors but are kept separate so per-stream
	// detection can diverge in future.
	styleErrSuccess  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleErrWarning  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleErrError    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleErrEmphasis = lipgloss.NewStyle().Bold(true)
	styleErrDim      = lipgloss.NewStyle().Foreground(_ld(lipgloss.Color("248"), lipgloss.Color("246")))
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

func Red(text string) string    { return render(ErrorStyle, text) }
func Green(text string) string  { return render(SuccessStyle, text) }
func Yellow(text string) string { return render(WarningStyle, text) }
func Cyan(text string) string   { return render(styleTeal, text) }
func Gray(text string) string   { return render(HelperStyle, text) }
func Bold(text string) string   { return render(TitleStyle, text) }
func Dim(text string) string    { return render(styleDim, text) }

func ErrGreen(text string) string  { return render(styleErrSuccess, text) }
func ErrYellow(text string) string { return render(styleErrWarning, text) }
func ErrBold(text string) string   { return render(styleErrEmphasis, text) }
func ErrDim(text string) string    { return render(styleErrDim, text) }
func ErrRed(text string) string    { return render(styleErrError, text) }
