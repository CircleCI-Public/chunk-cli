package watchd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func writePID(path string, p int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(p)+"\n"), 0o600)
}

func readPID(path string) (int, error) {
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
