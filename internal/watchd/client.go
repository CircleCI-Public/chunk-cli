package watchd

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

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
