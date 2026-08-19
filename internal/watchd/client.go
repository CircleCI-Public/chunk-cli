package watchd

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"
)

// FetchSnapshot connects to the watch daemon and returns the current snapshot
// for the given project roots. If roots is empty, all known projects are returned.
func FetchSnapshot(roots []string) (Snapshot, error) {
	sockPath, err := SocketPath()
	if err != nil {
		return Snapshot{}, err
	}
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return Snapshot{}, fmt.Errorf("connect to watch daemon: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := sendRequest(conn, wireRequest{Cmd: cmdSnapshot, Roots: roots}); err != nil {
		return Snapshot{}, fmt.Errorf("send snapshot request: %w", err)
	}
	resp, err := receiveResponse(conn)
	if err != nil {
		return Snapshot{}, fmt.Errorf("receive snapshot response: %w", err)
	}
	if !resp.OK {
		return Snapshot{}, fmt.Errorf("watch daemon error: %s", resp.Error)
	}
	if resp.Snapshot == nil {
		return Snapshot{}, nil
	}
	return *resp.Snapshot, nil
}

// ping returns true if the daemon's socket is accepting connections.
func ping(sockPath string) bool {
	conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// EnsureRunning checks whether the watch daemon is running, and launches it if
// not. The caller passes its own executable path and the subcommand arguments
// used to invoke the daemon (e.g. ["watch", "_daemon"]).
func EnsureRunning(subArgs []string) error {
	pidPath, err := PIDPath()
	if err != nil {
		return err
	}
	running, _, _ := IsRunning(pidPath)
	if running {
		return nil
	}
	return launchDaemon(subArgs)
}

func launchDaemon(subArgs []string) error {
	if _, err := EnsureDir(); err != nil {
		return fmt.Errorf("ensure watchd dir: %w", err)
	}
	logPath, err := LogPath()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable: %w", err)
	}

	child := exec.Command(executable, subArgs...)
	child.Stdout = logFile
	child.Stderr = logFile
	child.Stdin = nil
	detachProcess(child)
	if err := child.Start(); err != nil {
		return fmt.Errorf("start watch daemon: %w", err)
	}

	// Wait for the daemon to write its PID file and open its socket.
	pidPath, err := PIDPath()
	if err != nil {
		return err
	}
	sockPath, err := SocketPath()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok, _, _ := IsRunning(pidPath)
		if ok && ping(sockPath) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("watch daemon did not start within 5s; check %s", logPath)
}
