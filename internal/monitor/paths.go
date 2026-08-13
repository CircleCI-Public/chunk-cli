package monitor

import (
	"fmt"
	"os"
	"path/filepath"
)

func dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".chunk", "monitor"), nil
}

// EnsureDir creates ~/.chunk/monitor/ if it doesn't exist and returns the path.
func EnsureDir() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return d, os.MkdirAll(d, 0o700)
}

// PIDPath returns the path to the PID file for the named daemon.
func PIDPath(name string) (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name+".pid"), nil
}

// SocketPath returns the path to the Unix socket for the named daemon.
func SocketPath(name string) (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name+".sock"), nil
}

// DBPath returns the path to the server's SQLite database.
func DBPath() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "monitor.db"), nil
}

// AgentStatePath returns the path to the agent's JSON state file.
func AgentStatePath() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "agent-state.json"), nil
}

// LogPath returns the path to the log file for the named daemon.
func LogPath(name string) (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name+".log"), nil
}
