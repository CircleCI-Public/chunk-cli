// Package pid manages PID files for monitor daemons.
package pid

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Write writes p to path.
func Write(path string, p int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(p)+"\n"), 0o600)
}

// Read reads the PID stored in path.
func Read(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid pid in %s: %w", path, err)
	}
	return p, nil
}

// Running reports whether the process whose PID is in path is alive.
// Returns (false, 0, nil) when the file doesn't exist.
func Running(path string) (bool, int, error) {
	p, err := Read(path)
	if os.IsNotExist(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	proc, err := os.FindProcess(p)
	if err != nil {
		return false, 0, nil
	}
	if signalErr := proc.Signal(syscall.Signal(0)); signalErr != nil {
		return false, 0, nil
	}
	return true, p, nil
}

// Kill sends SIGTERM to the process whose PID is in path.
func Kill(path string) error {
	p, err := Read(path)
	if err != nil {
		return err
	}
	proc, err := os.FindProcess(p)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
