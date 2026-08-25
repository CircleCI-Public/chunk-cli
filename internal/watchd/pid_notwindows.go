//go:build !windows

package watchd

import (
	"os"
	"syscall"
)

// IsRunning reports whether the process whose PID is stored in path is alive.
// Returns (false, 0, nil) when the file doesn't exist.
func IsRunning(path string) (bool, int, error) {
	p, err := readPID(path)
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
