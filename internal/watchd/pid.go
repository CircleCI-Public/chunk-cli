package watchd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// WritePID records p as the daemon holding the pid file at path.
func WritePID(path string, p int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(p)+"\n"), 0o600)
}

// ReadPID returns the pid recorded in the pid file at path.
func ReadPID(path string) (int, error) {
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
