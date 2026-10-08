package chunkd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// legacyEnv is the name each variable had when the daemon was the watch
// daemon. The old name is still read when the new one is not set, so a shell or
// SSH tunnel set up before the rename keeps working; DeprecatedEnv names the
// ones in use so the CLI can say so.
var legacyEnv = map[string]string{ //nolint:gosec // variable names, not credentials
	EnvDir:        "CHUNK_WATCHD_DIR",
	EnvTCPAddr:    "CHUNK_WATCHD_TCP_ADDR",
	EnvRemoteAddr: "CHUNK_WATCHD_REMOTE_ADDR",
	EnvTCPToken:   "CHUNK_WATCHD_TCP_TOKEN",
}

// getenv reads name, falling back to its old name when name is not set at all.
// Setting the new name, even to empty, is what turns the old one off.
func getenv(name string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return os.Getenv(legacyEnv[name])
}

// DeprecatedEnv returns a warning for each old variable name that is being read
// because its new name is not set, in a stable order.
func DeprecatedEnv() []string {
	var warnings []string
	for _, name := range []string{EnvDir, EnvTCPAddr, EnvRemoteAddr, EnvTCPToken} {
		old := legacyEnv[name]
		if _, ok := os.LookupEnv(name); ok || os.Getenv(old) == "" {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("%s is deprecated and will stop working in a future release; set %s instead", old, name))
	}
	return warnings
}

// daemonDir is where the daemon keeps its socket, pid file and log.
func daemonDir() (string, error) {
	if override := getenv(EnvDir); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".chunk", "daemon"), nil
}

// EnsureDir creates the daemon's directory if it doesn't exist and returns the
// path. That is ~/.chunk/daemon/ unless CHUNK_DAEMON_DIR overrides it.
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

// legacyPaths returns the pid file and socket a daemon from before the rename
// would be using: watchd.pid and watchd.sock, in the directory the variables
// name or else in ~/.chunk/watchd/.
func legacyPaths() (pidPath, sockPath string, err error) {
	dir := getenv(EnvDir)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", fmt.Errorf("home dir: %w", err)
		}
		dir = filepath.Join(home, ".chunk", "watchd")
	}
	return filepath.Join(dir, "watchd.pid"), filepath.Join(dir, "watchd.sock"), nil
}

// TCPListenAddr returns the TCP address the daemon should bind, read from
// CHUNK_DAEMON_TCP_ADDR (e.g. "0.0.0.0:7777"). Empty means TCP is disabled.
func TCPListenAddr() string {
	return getenv(EnvTCPAddr)
}

// TCPRemoteAddr returns the address of a remote daemon to connect to, read
// from CHUNK_DAEMON_REMOTE_ADDR (e.g. "sandbox-host:7777"). Empty means
// clients use the local Unix socket.
func TCPRemoteAddr() string {
	return getenv(EnvRemoteAddr)
}

// TCPToken returns the bearer token required for TCP connections, read from
// CHUNK_DAEMON_TCP_TOKEN. When set, the daemon requires this token on every
// TCP request and clients include it in every request header.
func TCPToken() string {
	return getenv(EnvTCPToken)
}
