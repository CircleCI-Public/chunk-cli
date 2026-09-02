package watchd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
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

// ping reports whether the daemon at sockPath is reachable, along with the build
// identity it names. A daemon older than that identity reports "".
func ping(sockPath string) (bool, string) {
	resp, err := unixClient(sockPath).Get("http://watchd/ping")
	if err != nil {
		return false, ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, ""
	}
	// Bounded: the identity is short, and a body this side cannot recognise is
	// no reason to read an unbounded amount of it.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return true, ""
	}
	return true, strings.TrimSpace(string(body))
}

// stopDaemon asks the daemon to exit and waits until it stops answering, so the
// replacement does not race it for the socket.
func stopDaemon(pid int, sockPath string) error {
	if err := terminate(pid); err != nil {
		return fmt.Errorf("signal pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if reachable, _ := ping(sockPath); !reachable {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("watch daemon pid %d did not exit within 3s", pid)
}

// IsDaemonRunning reports whether the watch daemon is reachable.
func IsDaemonRunning() bool {
	sockPath, err := SocketPath()
	if err != nil {
		return false
	}
	ok, _ := ping(sockPath)
	return ok
}

// RunValidate delegates a validate run to the daemon. args is os.Args[1:];
// circleCIToken is forwarded to the subprocess as CIRCLE_TOKEN.
func RunValidate(args []string, circleCIToken string) (ValidateResponse, error) {
	sockPath, err := SocketPath()
	if err != nil {
		return ValidateResponse{}, err
	}
	body, err := json.Marshal(ValidateRequest{Args: args, CircleCIToken: circleCIToken, Env: os.Environ()})
	if err != nil {
		return ValidateResponse{}, fmt.Errorf("marshal validate request: %w", err)
	}
	resp, err := longUnixClient(sockPath).Post("http://watchd/validate", "application/json", bytes.NewReader(body))
	if err != nil {
		return ValidateResponse{}, fmt.Errorf("connect to watch daemon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return ValidateResponse{}, fmt.Errorf("watch daemon returned %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	var result ValidateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ValidateResponse{}, fmt.Errorf("decode validate response: %w", err)
	}
	return result, nil
}

// EnsureRunning checks whether the watch daemon is running and serving, and
// launches it if not. subArgs are the CLI arguments used to invoke the daemon
// (e.g. ["watch", "_daemon"]).
func EnsureRunning(subArgs []string) error {
	pidPath, err := PIDPath()
	if err != nil {
		return err
	}
	sockPath, err := SocketPath()
	if err != nil {
		return err
	}
	running, pid, err := IsRunning(pidPath)
	if err != nil {
		return fmt.Errorf("check running: %w", err)
	}
	if running {
		reachable, build := ping(sockPath)
		if reachable {
			if build == BuildID() {
				return nil
			}
			// A daemon from another build is replaced rather than reused: see
			// BuildID for why reusing it degrades silently.
			if stopErr := stopDaemon(pid, sockPath); stopErr != nil {
				return fmt.Errorf("replace stale watch daemon: %w", stopErr)
			}
		}
	}
	return launchDaemon(subArgs)
}

// EnsureLaunched starts the daemon when nothing is answering and otherwise
// leaves whatever is there alone.
//
// Unlike EnsureRunning it never replaces a daemon from another build. It is
// called when a poll fails mid-session, and a dashboard that has been open for a
// while has no business restarting a daemon another one is using: the build
// check is a startup decision, made once, where the cost of being wrong is one
// restart rather than a restart per poll for as long as two dashboards are open.
func EnsureLaunched(subArgs []string) error {
	pidPath, err := PIDPath()
	if err != nil {
		return err
	}
	sockPath, err := SocketPath()
	if err != nil {
		return err
	}
	running, _, err := IsRunning(pidPath)
	if err != nil {
		return fmt.Errorf("check running: %w", err)
	}
	if running {
		if reachable, _ := ping(sockPath); reachable {
			return nil
		}
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
		if reachable, _ := ping(sockPath); ok && reachable {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("watch daemon did not start within 5s; check %s", logPath)
}
