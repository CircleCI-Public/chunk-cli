package watchd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
