package watchd

import (
	"fmt"
	"os"
	"path/filepath"
)

func watchdDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".chunk", "watchd"), nil
}

// EnsureDir creates ~/.chunk/watchd/ if it doesn't exist and returns the path.
func EnsureDir() (string, error) {
	d, err := watchdDir()
	if err != nil {
		return "", err
	}
	return d, os.MkdirAll(d, 0o700)
}

// PIDPath returns the path to the daemon PID file.
func PIDPath() (string, error) {
	d, err := watchdDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "watchd.pid"), nil
}

// SocketPath returns the path to the daemon Unix socket.
func SocketPath() (string, error) {
	d, err := watchdDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "watchd.sock"), nil
}

// LogPath returns the path to the daemon log file.
func LogPath() (string, error) {
	d, err := watchdDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "watchd.log"), nil
}
