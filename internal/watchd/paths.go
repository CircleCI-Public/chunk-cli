package watchd

import (
	"fmt"
	"os"
	"path/filepath"
)

func watchdDir() (string, error) {
	if override := os.Getenv("CHUNK_WATCHD_DIR"); override != "" {
		return override, nil
	}
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

// TCPListenAddr returns the TCP address the daemon should bind, read from
// CHUNK_WATCHD_TCP_ADDR (e.g. "0.0.0.0:7777"). Empty means TCP is disabled.
func TCPListenAddr() string {
	return os.Getenv("CHUNK_WATCHD_TCP_ADDR")
}

// TCPRemoteAddr returns the address of a remote daemon to connect to, read
// from CHUNK_WATCHD_REMOTE_ADDR (e.g. "sandbox-host:7777"). Empty means
// clients use the local Unix socket.
func TCPRemoteAddr() string {
	return os.Getenv("CHUNK_WATCHD_REMOTE_ADDR")
}

// TCPToken returns the bearer token required for TCP connections, read from
// CHUNK_WATCHD_TCP_TOKEN. When set, the daemon requires this token on every
// TCP request and clients include it in every request header.
func TCPToken() string {
	return os.Getenv("CHUNK_WATCHD_TCP_TOKEN")
}
