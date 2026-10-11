package smeltd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/smeltdev/smelt/internal/brand"
)

// maxSocketPath returns the size of the kernel's Unix socket path buffer: 104
// bytes on macOS and the BSDs, 108 on Linux. The path and its terminating NUL
// must fit, so a path of that many bytes or more cannot be bound.
func maxSocketPath() int {
	switch runtime.GOOS {
	case "darwin", "freebsd", "netbsd", "openbsd", "dragonfly":
		return 104
	}
	return 108
}

// checkSocketPath fails with an actionable message when path is too long to bind
// as a Unix socket. Without this the daemon dies with "bind: invalid argument"
// in its own log and the CLI can only report that the daemon did not start.
func checkSocketPath(path string) error {
	limit := maxSocketPath()
	if len(path) < limit {
		return nil
	}
	return fmt.Errorf("the smelt daemon's socket path is too long (%d bytes, the limit is %d): %s\n"+
		"set SMELT_DAEMON_DIR to a shorter directory, for example SMELT_DAEMON_DIR=/tmp/smelt-daemon",
		len(path), limit-1, path)
}

// The variables that say where the daemon is and how to reach it.
const (
	EnvDir        = brand.EnvPrefix + "DAEMON_DIR"
	EnvTCPAddr    = brand.EnvPrefix + "DAEMON_TCP_ADDR"
	EnvRemoteAddr = brand.EnvPrefix + "DAEMON_REMOTE_ADDR"
	EnvTCPToken   = brand.EnvPrefix + "DAEMON_TCP_TOKEN" //nolint:gosec // the variable's name, not a credential
)

// daemonDir is where the daemon keeps its socket, pid file and log.
func daemonDir() (string, error) {
	if override := os.Getenv(EnvDir); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, brand.HomeDir, "daemon"), nil
}

// EnsureDir creates the daemon's directory if it doesn't exist and returns the
// path. That is ~/.smelt/daemon/ unless SMELT_DAEMON_DIR overrides it.
func EnsureDir() (string, error) {
	d, err := daemonDir()
	if err != nil {
		return "", err
	}
	return d, os.MkdirAll(d, 0o700)
}

// PIDPath returns the path to the daemon PID file.
func PIDPath() (string, error) {
	d, err := daemonDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "daemon.pid"), nil
}

// SocketPath returns the path to the daemon Unix socket.
func SocketPath() (string, error) {
	d, err := daemonDir()
	if err != nil {
		return "", err
	}
	sock := filepath.Join(d, "daemon.sock")
	if err := checkSocketPath(sock); err != nil {
		return "", err
	}
	return sock, nil
}

// LogPath returns the path to the daemon log file.
func LogPath() (string, error) {
	d, err := daemonDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "daemon.log"), nil
}

// TCPListenAddr returns the TCP address the daemon should bind, read from
// SMELT_DAEMON_TCP_ADDR (e.g. "0.0.0.0:7777"). Empty means TCP is disabled.
func TCPListenAddr() string {
	return os.Getenv(EnvTCPAddr)
}

// TCPRemoteAddr returns the address of a remote daemon to connect to, read
// from SMELT_DAEMON_REMOTE_ADDR (e.g. "sandbox-host:7777"). Empty means
// clients use the local Unix socket.
func TCPRemoteAddr() string {
	return os.Getenv(EnvRemoteAddr)
}

// TCPToken returns the bearer token required for TCP connections, read from
// SMELT_DAEMON_TCP_TOKEN. When set, the daemon requires this token on every
// TCP request and clients include it in every request header.
func TCPToken() string {
	return os.Getenv(EnvTCPToken)
}
