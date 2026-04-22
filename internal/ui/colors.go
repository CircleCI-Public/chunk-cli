package ui

import (
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

var colorEnabled = detectColor()

func detectColor() bool {
	if os.Getenv(config.EnvNoColor) != "" {
		return false
	}
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// SetColorEnabled overrides automatic color detection.
func SetColorEnabled(enabled bool) {
	colorEnabled = enabled
}

func wrap(code, text string) string {
	if !colorEnabled {
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
