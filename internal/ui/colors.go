package ui

import (
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

var (
	stdoutColorEnabled = detectColorFor(os.Stdout)
	stderrColorEnabled = detectColorFor(os.Stderr)
)

func detectColorFor(f *os.File) bool {
	if os.Getenv(config.EnvNoColor) != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// SetColorEnabled overrides automatic color detection for both stdout and stderr.
func SetColorEnabled(enabled bool) {
	stdoutColorEnabled = enabled
	stderrColorEnabled = enabled
}

func wrap(code, text string) string {
	if !stdoutColorEnabled {
		return text
	}
	return fmt.Sprintf("\x1b[%sm%s\x1b[0m", code, text)
}

func wrapErr(code, text string) string {
	if !stderrColorEnabled {
		return text
	}
	return fmt.Sprintf("\x1b[%sm%s\x1b[0m", code, text)
}

func Red(text string) string    { return wrap("31", text) }
func Green(text string) string  { return wrap("32", text) }
func Yellow(text string) string { return wrap("33", text) }
func Cyan(text string) string   { return wrap("36", text) }
func Gray(text string) string   { return wrap("90", text) }
func Bold(text string) string   { return wrap("1", text) }
func Dim(text string) string    { return wrap("2", text) }

// ErrGreen applies green color using stderr color detection.
func ErrGreen(text string) string { return wrapErr("32", text) }

// ErrYellow applies yellow color using stderr color detection.
func ErrYellow(text string) string { return wrapErr("33", text) }

// ErrBold applies bold using stderr color detection.
func ErrBold(text string) string { return wrapErr("1", text) }

// ErrDim applies dim using stderr color detection.
func ErrDim(text string) string { return wrapErr("2", text) }
