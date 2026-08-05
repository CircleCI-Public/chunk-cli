package tui

import (
	"errors"
	"os"

	"golang.org/x/term"
)

// ErrNoTTY is returned when a TUI prompt is called without an interactive terminal.
var ErrNoTTY = errors.New("no interactive terminal available")

// requireTTY returns ErrNoTTY if stdin is not an interactive terminal.
func requireTTY() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return ErrNoTTY
	}
	return nil
}

// RequireStdoutTTY returns ErrNoTTY if stdout is not an interactive terminal.
// Use this for full-screen TUI programs that write to stdout.
func RequireStdoutTTY() error {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return ErrNoTTY
	}
	return nil
}
