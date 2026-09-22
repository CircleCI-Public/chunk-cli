package watchd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrDaemonUnavailable is returned by RunValidate when the daemon socket is
// unreachable, so callers can distinguish a transient connectivity failure from
// a real validation error and fall back to inline execution.
var ErrDaemonUnavailable = errors.New("daemon unavailable")

// daemonClient returns an http.Client for the running daemon. When
// CHUNK_WATCHD_REMOTE_ADDR is set it connects over TCP; otherwise it uses the
// local Unix socket. Both variants ignore the URL hostname and always connect
// to the configured address, so callers keep using "http://watchd/..." URLs.
func daemonClient() (*http.Client, error) {
	if addr := TCPRemoteAddr(); addr != "" {
		return tcpClient(addr), nil
	}
	sockPath, err := SocketPath()
	if err != nil {
		return nil, err
	}
	return unixClient(sockPath), nil
}

// longDaemonClient is like daemonClient but without a total-request timeout,
// suitable for long-running operations like /validate.
func longDaemonClient() (*http.Client, error) {
	if addr := TCPRemoteAddr(); addr != "" {
		return longTCPClient(addr), nil
	}
	sockPath, err := SocketPath()
	if err != nil {
		return nil, err
	}
	return longUnixClient(sockPath), nil
}

// doPing sends a /ping request with the given client and returns reachability
// and the daemon's build identity.
func doPing(client *http.Client) (bool, string) {
	resp, err := client.Get("http://watchd/ping")
	if err != nil {
		return false, ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		log.Printf("watchd: daemon rejected request — check CHUNK_WATCHD_TCP_TOKEN")
		return false, ""
	}
	if resp.StatusCode != http.StatusOK {
		return false, ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return true, ""
	}
	return true, strings.TrimSpace(string(body))
}

// pingDaemon pings via the configured transport (TCP when
// CHUNK_WATCHD_REMOTE_ADDR is set, Unix socket otherwise).
func pingDaemon() (bool, string) {
	client, err := daemonClient()
	if err != nil {
		return false, ""
	}
	return doPing(client)
}

// FetchSnapshot connects to the running watch daemon and returns the current
// snapshot for the given project roots. If roots is empty all known projects
// are returned.
func FetchSnapshot(roots []string) (Snapshot, error) {
	client, err := daemonClient()
	if err != nil {
		return Snapshot{}, err
	}
	body, err := json.Marshal(roots)
	if err != nil {
		return Snapshot{}, fmt.Errorf("marshal roots: %w", err)
	}
	resp, err := client.Post("http://watchd/snapshot", "application/json", bytes.NewReader(body))
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

// registerTimeout bounds a command registration. It is deliberately short: this
// call sits on the hook path, in front of a command the developer is waiting for,
// and a logs pane is never worth delaying that.
const registerTimeout = 2 * time.Second

// RegisterCommand tells the running watch daemon to stream and buffer a command's
// output.
//
// It is best-effort by design and reports no error. If the daemon is not running,
// the command still runs and still streams to the caller's own stdout; the only
// thing lost is the buffered copy. Notably this does not start the daemon:
// spawning a background process as a side effect of a hook firing is intrusive,
// and a hook that hangs waiting for a daemon launch is a far worse failure than a
// missing logs pane.
func RegisterCommand(reg CommandReg) {
	client, err := daemonClient()
	if err != nil {
		return
	}
	body, err := json.Marshal(reg)
	if err != nil {
		return
	}
	client.Timeout = registerTimeout
	resp, err := client.Post("http://watchd/command", "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// conflictTimeout bounds a conflict query. Short for the same reason
// registerTimeout is: this call sits on the hook path in front of work the
// developer is waiting for, and an advisory notice is never worth delaying it.
const conflictTimeout = 2 * time.Second

// ErrDaemonUnreachable reports that no watch daemon answered.
//
// Callers on the hook path are expected to treat this as "nothing to say" and
// carry on. The daemon is optional — it runs when the developer has `chunk
// watch` open — and a hook that complains about its absence would fire on every
// session end for everyone who does not.
var ErrDaemonUnreachable = errors.New("no watch daemon is running")

// ErrDaemonTimeout reports that a daemon was there but did not answer within
// conflictTimeout.
//
// Kept apart from ErrDaemonUnreachable because the two call for different
// advice: one means start the daemon, the other means it is already running and
// busy, so asking again is what helps. Collapsing them told people with a
// working daemon to go start one.
var ErrDaemonTimeout = errors.New("the watch daemon did not answer in time")

// FetchConflicts asks the running daemon whether root's branch still merges
// cleanly into its merge target.
//
// Unlike RegisterCommand this reports its errors, because the caller decides
// how loudly to fail: a hook stays quiet, a person running the command by hand
// gets told why there is no answer.
func FetchConflicts(root string) (ConflictReport, error) {
	sockPath, err := SocketPath()
	if err != nil {
		return ConflictReport{}, err
	}
	reqURL := "http://watchd/conflicts?root=" + neturl.QueryEscape(root)
	client := unixClient(sockPath)
	client.Timeout = conflictTimeout
	resp, err := client.Get(reqURL)
	if err != nil {
		// A refused connection and an expired deadline are different answers.
		// os.IsTimeout sees through the *url.Error the client wraps around it,
		// which is why the check is not an errors.Is against a sentinel.
		if os.IsTimeout(err) {
			return ConflictReport{}, ErrDaemonTimeout
		}
		return ConflictReport{}, ErrDaemonUnreachable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ConflictReport{}, fmt.Errorf("watch daemon returned %s", resp.Status)
	}
	var report ConflictReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return ConflictReport{}, fmt.Errorf("decode conflicts: %w", err)
	}
	return report, nil
}

// FetchOutput reads buffered output for a command starting at offset.
func FetchOutput(commandID string, offset int64) (OutputChunk, error) {
	client, err := daemonClient()
	if err != nil {
		return OutputChunk{}, err
	}
	reqURL := fmt.Sprintf("http://watchd/output?command_id=%s&offset=%d",
		neturl.QueryEscape(commandID), offset)
	resp, err := client.Get(reqURL)
	if err != nil {
		return OutputChunk{}, fmt.Errorf("connect to watch daemon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return OutputChunk{}, fmt.Errorf("watch daemon returned %s", resp.Status)
	}
	var chunk OutputChunk
	if err := json.NewDecoder(resp.Body).Decode(&chunk); err != nil {
		return OutputChunk{}, fmt.Errorf("decode output: %w", err)
	}
	return chunk, nil
}

// ping reports whether the daemon at sockPath is reachable, along with the
// build identity it names. A daemon older than that identity reports "".
func ping(sockPath string) (bool, string) {
	return doPing(unixClient(sockPath))
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

// StopForCredentialChange stops a running watch daemon so that the next launch
// picks up newly stored credentials.
//
// The daemon resolves its CircleCI client once, at startup, so one that started
// before a login holds a nil client for the rest of its life and streams no
// output however many times the developer retries. Stopping it here is what
// makes `chunk auth login` take effect: a `chunk watch` already on screen
// relaunches it through EnsureLaunched on its next poll, and otherwise the next
// `chunk watch` starts a daemon that can authenticate.
//
// Best-effort and silent, like RegisterCommand. Failing to stop the daemon must
// not fail a login that has otherwise succeeded, and the cost of not stopping it
// is the buffered output of a daemon that was not streaming anything anyway.
func StopForCredentialChange() {
	// A remote daemon is managed externally; we have no way to restart it, and
	// the local pid file / socket are unrelated to it.
	if TCPRemoteAddr() != "" {
		return
	}
	pidPath, err := PIDPath()
	if err != nil {
		return
	}
	sockPath, err := SocketPath()
	if err != nil {
		return
	}
	running, pid, err := IsRunning(pidPath)
	if err != nil || !running {
		return
	}
	// Only stop something that is actually answering: a stale pid file is the
	// launcher's problem to clean up, not ours.
	if reachable, _ := ping(sockPath); !reachable {
		return
	}
	_ = stopDaemon(pid, sockPath)
}

// IsDaemonRunning reports whether the watch daemon is reachable. Use
// IsDaemonCompatible when the caller needs to confirm the build identity too.
func IsDaemonRunning() bool {
	ok, _ := pingDaemon()
	return ok
}

// IsDaemonCompatible reports whether the watch daemon is reachable and suitable
// for delegation. For a local daemon that means matching the current build (a
// build mismatch means it may not support all API endpoints). For a remote
// daemon the build ID can never match — the binary lives on a different host
// with a different path and mtime — so reachability is the meaningful check.
func IsDaemonCompatible() bool {
	ok, build := pingDaemon()
	if !ok {
		return false
	}
	if TCPRemoteAddr() != "" {
		return true
	}
	return build == BuildID()
}

// RunValidate delegates a validate run to the daemon. req.ProjectRoot is the
// repo to validate, already resolved by the caller.
//
// The caller is responsible for deciding which fields to populate: req.Env and
// req.CircleCIToken should only be set when using the local Unix socket — over
// TCP the remote daemon uses its own credentials and environment. See
// runValidateViaDaemon in cmd/ for the canonical call site.
//
// Set req.AllowAsync to offer the daemon the option of releasing this caller
// and reporting later; a response carrying a TaskID is that offer taken, and
// means nothing has run yet.
//
// It takes the request type rather than a list of arguments because what the
// daemon needs to know about a run keeps growing, and every addition would
// otherwise be another positional parameter at two call sites.
func RunValidate(req ValidateRequest) (ValidateResponse, error) {
	client, err := longDaemonClient()
	if err != nil {
		return ValidateResponse{}, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return ValidateResponse{}, fmt.Errorf("marshal validate request: %w", err)
	}
	resp, err := client.Post("http://watchd/validate", "application/json", bytes.NewReader(body))
	if err != nil {
		return ValidateResponse{}, fmt.Errorf("%w: %w", ErrDaemonUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		err := fmt.Errorf("watch daemon returned %s: %s", resp.Status, bytes.TrimSpace(msg))
		// 404 means the daemon is running but does not have the /validate
		// endpoint — it is from an older build. Treat it as unavailable so
		// callers fall back to inline execution instead of surfacing the error.
		if resp.StatusCode == http.StatusNotFound {
			return ValidateResponse{}, fmt.Errorf("%w: %w", ErrDaemonUnavailable, err)
		}
		return ValidateResponse{}, err
	}
	var result ValidateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ValidateResponse{}, fmt.Errorf("decode validate response: %w", err)
	}
	return result, nil
}

// ErrAsyncRefused is returned by StartAsyncValidate when the daemon will not
// take this run asynchronously: the tree could not be fingerprinted, so a
// stale result would be undetectable, or the project already has its cap of
// runs in flight. Callers fall back to running inline, where the answer
// reaches whoever asked for it while it is still true. The daemon's reason is
// wrapped alongside the sentinel.
var ErrAsyncRefused = errors.New("async validation refused")

// StartAsyncValidate asks the daemon to validate projectRoot in the background
// and returns the new task's ID without waiting for the run.
func StartAsyncValidate(projectRoot string, args []string, circleCIToken string) (string, error) {
	sockPath, err := SocketPath()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(ValidateRequest{
		Args:          args,
		CircleCIToken: circleCIToken,
		Env:           os.Environ(),
		ProjectRoot:   projectRoot,
	})
	if err != nil {
		return "", fmt.Errorf("marshal async validate request: %w", err)
	}
	// The short client is right here: this call returns as soon as the run is
	// accepted, so it must not inherit the no-timeout client /validate needs.
	resp, err := unixClient(sockPath).Post("http://watchd/validate/async", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDaemonUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusAccepted:
		var result AsyncValidateResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return "", fmt.Errorf("decode async validate response: %w", err)
		}
		return result.TaskID, nil
	case http.StatusConflict:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("%w: %s", ErrAsyncRefused, bytes.TrimSpace(msg))
	case http.StatusNotFound:
		// A daemon from a build without this endpoint. Unavailable, so the caller
		// runs inline rather than surfacing an error.
		return "", fmt.Errorf("%w: daemon has no /validate/async endpoint", ErrDaemonUnavailable)
	default:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("watch daemon returned %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
}

// CollectValidateResults returns the finished async results for projectRoot and
// clears them from the daemon, so a result is reported once and not repeated.
//
// Best-effort: with no daemon running there is nothing to collect and nothing to
// report, which is not an error worth surfacing on a hook path.
func CollectValidateResults(projectRoot string) ([]TaskState, error) {
	sockPath, err := SocketPath()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDaemonUnavailable, err)
	}
	url := "http://watchd/validate/collect?root=" + neturl.QueryEscape(projectRoot)
	resp, err := unixClient(sockPath).Get(url)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDaemonUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: daemon has no /validate/collect endpoint", ErrDaemonUnavailable)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("watch daemon returned %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	var result CollectResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode collect response: %w", err)
	}
	return result.Tasks, nil
}

// EnsureRunning checks whether the watch daemon is running and serving, and
// launches it if not. subArgs are the CLI arguments used to invoke the daemon
// (e.g. ["watch", "_daemon"]).
func EnsureRunning(subArgs []string) error {
	// Remote daemons are managed externally; local pid/socket operations are
	// irrelevant and would start a stray local daemon.
	if TCPRemoteAddr() != "" {
		return nil
	}
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
	// Remote daemons are managed externally; starting a local one would be wrong.
	if TCPRemoteAddr() != "" {
		return nil
	}
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
