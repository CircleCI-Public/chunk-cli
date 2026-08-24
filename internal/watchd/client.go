package watchd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

// FetchSnapshot connects to the running watch daemon and returns the current
// snapshot for the given project roots. If roots is empty all known projects
// are returned.
func FetchSnapshot(roots []string) (Snapshot, error) {
	sockPath, err := SocketPath()
	if err != nil {
		return Snapshot{}, err
	}
	body, err := json.Marshal(roots)
	if err != nil {
		return Snapshot{}, fmt.Errorf("marshal roots: %w", err)
	}
	resp, err := unixClient(sockPath).Post("http://watchd/snapshot", "application/json", bytes.NewReader(body))
	if err != nil {
		return Snapshot{}, fmt.Errorf("connect to watch daemon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("watch daemon returned %s", resp.Status)
	}
	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot: %w", err)
	}
	return snap, nil
}

// ping returns true if the daemon at sockPath is reachable.
func ping(sockPath string) bool {
	resp, err := unixClient(sockPath).Get("http://watchd/ping")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == 200
}

// EnsureRunning checks whether the watch daemon is running and launches it if not.
// subArgs are the CLI arguments used to invoke the daemon (e.g. ["watch", "_daemon"]).
func EnsureRunning(subArgs []string) error {
	pidPath, err := PIDPath()
	if err != nil {
		return err
	}
	running, _, err := IsRunning(pidPath)
	if err != nil {
		return fmt.Errorf("check running: %w", err)
	}
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
