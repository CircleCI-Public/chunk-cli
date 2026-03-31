package tui

import (
	"errors"
	"os"

	"golang.org/x/term"
)

// ErrNoTTY is returned when a TUI prompt is called without an interactive terminal.
var ErrNoTTY = errors.New("no interactive terminal available")

// requireTTY returns ErrNoTTY if stdin is not a terminal.
func requireTTY() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return ErrNoTTY
	}
	return nil
}
