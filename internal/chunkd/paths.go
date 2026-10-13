package chunkd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
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
	return fmt.Errorf("the chunk daemon's socket path is too long (%d bytes, the limit is %d): %s\n"+
		"set CHUNK_DAEMON_DIR to a shorter directory, for example CHUNK_DAEMON_DIR=/tmp/chunk-daemon",
		len(path), limit-1, path)
}

// The variables that say where the daemon is and how to reach it.
const (
	EnvDir        = "CHUNK_DAEMON_DIR"
	EnvTCPAddr    = "CHUNK_DAEMON_TCP_ADDR"
	EnvRemoteAddr = "CHUNK_DAEMON_REMOTE_ADDR"
	EnvTCPToken   = "CHUNK_DAEMON_TCP_TOKEN" //nolint:gosec // the variable's name, not a credential
)

// daemonDir is where the daemon keeps its socket, pid file and log.
func daemonDir() (string, error) {
	if override := os.Getenv(EnvDir); override != "" {
		return override, nil
	}
	state, err := config.AppState()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "daemon"), nil
}

// EnsureDir creates the daemon's directory if it doesn't exist and returns the
// path. That is $XDG_STATE_HOME/chunk/daemon/ unless CHUNK_DAEMON_DIR overrides it.
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

// findDaemon returns the pid file and socket of the local daemon a client should
// talk to: the ones under daemonDir, unless nothing answers there and a daemon
// started by an older chunk answers under ~/.chunk/daemon. That daemon is reused
// until it exits; a new one always starts under daemonDir.
func findDaemon() (pidPath, sockPath string, err error) {
	pidPath, err = PIDPath()
	if err != nil {
		return "", "", err
	}
	sockPath, err = SocketPath()
	if err != nil || os.Getenv(EnvDir) != "" || accepts(sockPath) {
		return pidPath, sockPath, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return pidPath, sockPath, nil
	}
	legacy := filepath.Join(home, ".chunk", "daemon")
	if legacySock := filepath.Join(legacy, "daemon.sock"); accepts(legacySock) {
		return filepath.Join(legacy, "daemon.pid"), legacySock, nil
	}
	return pidPath, sockPath, nil
}

// accepts reports whether something is listening on the Unix socket at path.
func accepts(path string) bool {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
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
// CHUNK_DAEMON_TCP_ADDR (e.g. "0.0.0.0:7777"). Empty means TCP is disabled.
func TCPListenAddr() string {
	return os.Getenv(EnvTCPAddr)
}

// TCPRemoteAddr returns the address of a remote daemon to connect to, read
// from CHUNK_DAEMON_REMOTE_ADDR (e.g. "sandbox-host:7777"). Empty means
// clients use the local Unix socket.
func TCPRemoteAddr() string {
	return os.Getenv(EnvRemoteAddr)
}

// TCPToken returns the bearer token required for TCP connections, read from
// CHUNK_DAEMON_TCP_TOKEN. When set, the daemon requires this token on every
// TCP request and clients include it in every request header.
func TCPToken() string {
	return os.Getenv(EnvTCPToken)
}
