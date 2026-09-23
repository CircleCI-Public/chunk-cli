package sidecar

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// proxyErrDrainTimeout bounds how long a failed rsync waits for the SSH proxy
// goroutine to report why it gave up. Long enough for an in-flight WebSocket
// dial to resolve after the proxy is cancelled, short enough not to be felt.
const proxyErrDrainTimeout = 200 * time.Millisecond

// RsyncSync syncs the local working tree (rooted at cwd) to a sidecar using
// rsync over an SSH-over-WebSocket tunnel. Files matching .gitignore rules are
// excluded. In a normal checkout, .git is copied so the remote has a fully
// functional git repo. In a git worktree, .git is a local pointer file that
// would be broken on the sidecar, so it is excluded and a fresh git repo is
// initialised on the remote instead. workdir overrides the destination path;
// defaults to /home/user/<repo name from git remote>.
func RsyncSync(ctx context.Context,
	client *circleci.Client, sidecarID, workdir, cwd string,
	status iostream.StatusFunc) error {

	return rsyncTo(ctx, client, sidecarID, workdir, cwd, true, status)
}

// RsyncSyncEphemeral syncs like RsyncSync but neither reads nor writes the
// active sidecar file. workdir is required.
func RsyncSyncEphemeral(ctx context.Context,
	client *circleci.Client, sidecarID, workdir, cwd string,
	status iostream.StatusFunc) error {

	if workdir == "" {
		return fmt.Errorf("rsync: workdir is required for an ephemeral sync")
	}
	return rsyncTo(ctx, client, sidecarID, workdir, cwd, false, status)
}

func rsyncTo(ctx context.Context, client *circleci.Client,
	sidecarID, workdir, cwd string, persist bool,
	status iostream.StatusFunc) error {

	sess, err := OpenSession(ctx, client, sidecarID, false)
	if err != nil {
		return fmt.Errorf("rsync: open session: %w", err)
	}

	// Detect a git worktree by walking up from cwd to find the git root, then
	// checking whether .git there is a file (worktree pointer) or a directory
	// (normal checkout). Walking up ensures detection works when chunk is run
	// from a subdirectory of the worktree.
	gitRoot, _ := findGitRootFrom(cwd)
	var isWorktree bool
	if gitRoot != "" {
		if gitInfo, statErr := os.Stat(filepath.Join(gitRoot, ".git")); statErr == nil {
			isWorktree = !gitInfo.IsDir()
		}
	}

	// In a worktree, fetch the origin URL once; it is used for workspace
	// resolution (to parse the repo name) and to configure the remote on the
	// sidecar after init.
	var worktreeOriginURL string
	if isWorktree {
		out, execErr := exec.CommandContext(ctx, "git", "-C", gitRoot, "remote", "get-url", "origin").Output()
		if execErr != nil {
			status(iostream.LevelWarn, fmt.Sprintf("Could not read git remote origin: %v", execErr))
		} else {
			worktreeOriginURL = strings.TrimSpace(string(out))
		}
	}

	repoPath := workdir
	if persist {
		repoPath, err = rsyncWorkspace(ctx, workdir, cwd, worktreeOriginURL, isWorktree, status)
		if err != nil {
			return err
		}
		if err := persistWorkspace(ctx, repoPath); err != nil {
			status(iostream.LevelWarn, fmt.Sprintf("Could not save workspace: %v", err))
		}
	}

	status(iostream.LevelInfo, fmt.Sprintf("Syncing workspace %s...", repoPath))

	if check, err := ExecOverSSH(ctx, sess, "which rsync", nil, nil); err != nil {
		return fmt.Errorf("rsync: check remote rsync: %w", err)
	} else if check.ExitCode != 0 {
		if result, err := ExecOverSSH(ctx, sess, "sudo apt-get update -qq", nil, nil); err != nil {
			return fmt.Errorf("rsync: apt-get update: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("rsync: apt-get update: exit %d: %s", result.ExitCode, result.Stderr)
		}
		if result, err := ExecOverSSH(ctx, sess, "sudo apt-get install -y -qq rsync", nil, nil); err != nil {
			return fmt.Errorf("rsync: install rsync on sidecar: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("rsync: install rsync on sidecar: exit %d: %s", result.ExitCode, result.Stderr)
		}
	}

	if result, err := ExecOverSSH(ctx, sess, "mkdir -p "+ShellEscape(repoPath), nil, nil); err != nil {
		return fmt.Errorf("rsync: mkdir: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("rsync: mkdir %s: %s", repoPath, result.Stderr)
	}

	localAddr, stopProxy, proxyErr, err := startSSHProxy(ctx, sess)
	if err != nil {
		return fmt.Errorf("rsync: start SSH proxy: %w", err)
	}
	defer stopProxy()

	_, port, err := net.SplitHostPort(localAddr)
	if err != nil {
		return fmt.Errorf("rsync: parse proxy addr: %w", err)
	}

	sshCmd := sshCommand(sess, port)

	src := strings.TrimRight(cwd, "/") + "/"
	dst := fmt.Sprintf("%s@127.0.0.1:%s", defaultSSHUser, repoPath)

	rsyncArgs := []string{
		"--archive",
		"--delete",
		"--filter=:- .gitignore",
	}
	if isWorktree {
		// Exclude the .git pointer file: rsync --delete does not remove excluded
		// destination files, so a stale pointer from a previous run stays on the
		// sidecar. We clean it up and rebuild a proper git repo after the sync.
		rsyncArgs = append(rsyncArgs, "--exclude=/.git")
	}
	rsyncArgs = append(rsyncArgs, "-e", sshCmd, src, dst)

	cmd := exec.CommandContext(ctx, "rsync", rsyncArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := rsyncErrDetail(stderr.String())
		// Stop the proxy before reading proxyErr. bridgeConn only sends once
		// its WebSocket dial resolves, so a non-blocking read here would drop
		// the proxy error whenever rsync exited for an unrelated reason (its
		// own timeout, a signal) while the dial was still in flight.
		stopProxy()
		var proxyDetail string
		select {
		case pe := <-proxyErr:
			proxyDetail = pe.Error()
		case <-time.After(proxyErrDrainTimeout):
		}
		switch {
		case detail != "" && proxyDetail != "":
			return fmt.Errorf("rsync: %w\n%s\nproxy: %s", err, detail, proxyDetail)
		case proxyDetail != "":
			return fmt.Errorf("rsync: %w\nproxy: %s", err, proxyDetail)
		case detail != "":
			return fmt.Errorf("rsync: %w\n%s", err, detail)
		default:
			return fmt.Errorf("rsync: %w", err)
		}
	}

	if isWorktree {
		if err := initWorktreeGitRepo(ctx, sess, repoPath, worktreeOriginURL); err != nil {
			return fmt.Errorf("rsync: %w", err)
		}
	}

	status(iostream.LevelDone, "Synced")
	return nil
}

// rsyncWorkspace determines the sidecar workspace path for a sync. In a git
// worktree with no --workdir override it derives the path from the origin URL,
// deliberately skipping the saved workspace: older versions recorded the
// worktree directory name there, which no other code path agrees with.
// Otherwise it defers to ResolveWorkspace.
func rsyncWorkspace(ctx context.Context, workdir, cwd, originURL string, isWorktree bool,
	status iostream.StatusFunc) (string, error) {

	inWorktree := isWorktree && workdir == ""
	if inWorktree {
		if ws, ok := worktreeWorkspace(originURL); ok {
			return ws, nil
		}
		// Empty or unusable origin URL — fall through to normal resolution.
	}

	_, repo, repoErr := gitremote.DetectOrgAndRepo(cwd)
	if repoErr != nil {
		repo = filepath.Base(cwd)
		if inWorktree {
			// The worktree directory name is not the repo name, so this path will
			// not match the one other code paths use. Say so rather than silently
			// syncing to the wrong workspace.
			status(iostream.LevelWarn, fmt.Sprintf(
				"Could not determine the repo name from git; using the directory name %q as the sidecar workspace.", repo))
		}
	}
	repoPath, err := ResolveWorkspace(ctx, workdir, repo)
	if err != nil {
		return "", fmt.Errorf("rsync: resolve workspace: %w", err)
	}
	return repoPath, nil
}

// worktreeWorkspace returns the sidecar workspace path for a git worktree by
// parsing the repo name from originURL. Returns ("", false) when the URL is
// empty or yields no usable repo name, so the caller can fall back to
// ResolveWorkspace.
func worktreeWorkspace(originURL string) (string, bool) {
	if originURL == "" {
		return "", false
	}
	if _, repo, err := gitremote.ParseRemoteURL(originURL); err == nil && repo != "" {
		return DefaultWorkspace(repo), true
	}
	// Non-GitHub remote (GitHub Enterprise, GitLab, a plain local path). The repo
	// name is still the last path segment, which is what the sidecar workspace
	// should be named after — and far better than the worktree directory name.
	if repo, ok := repoNameFromURL(originURL); ok {
		return DefaultWorkspace(repo), true
	}
	return "", false
}

// repoNameFromURL extracts a repo name from an arbitrary git remote URL by
// taking its last path segment and stripping a trailing .git. It handles scp
// style remotes (git@host:org/repo.git) as well as URLs and local paths.
// Returns ("", false) when no plausible name can be recovered.
func repoNameFromURL(originURL string) (string, bool) {
	trimmed := strings.TrimRight(strings.TrimSpace(originURL), "/")
	// Drop any scp style host prefix so "git@host:repo.git" yields "repo".
	if idx := strings.LastIndex(trimmed, ":"); idx != -1 {
		trimmed = trimmed[idx+1:]
	}
	repo := strings.TrimSuffix(path.Base(trimmed), ".git")
	// path.Base returns "." for an empty input and "/" for a root-only path.
	if repo == "" || repo == "." || repo == "/" {
		return "", false
	}
	return repo, true
}

// initWorktreeGitRepo sets up a minimal git repo in repoPath on the sidecar
// after an rsync that excluded the .git pointer file. On the first sync it
// removes any stale .git entry and runs git init; on subsequent syncs the
// existing .git directory is reused. The origin URL is set (or updated) each
// time so that git remote detection works inside the workspace.
func initWorktreeGitRepo(ctx context.Context, sess *Session, repoPath, originURL string) error {
	// Only initialise if there is no .git directory yet. This avoids rebuilding
	// sidecar-side git state on every sync.
	check, err := ExecOverSSH(ctx, sess, "test -d "+ShellEscape(repoPath+"/.git"), nil, nil)
	if err != nil {
		return fmt.Errorf("check .git on sidecar: %w", err)
	}
	if check.ExitCode != 0 {
		// No .git directory — remove any stale pointer file left by a previous sync
		// and initialise a fresh repo.
		if result, err := ExecOverSSH(ctx, sess, "rm -rf "+ShellEscape(repoPath+"/.git"), nil, nil); err != nil {
			return fmt.Errorf("remove stale .git on sidecar: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("remove stale .git on sidecar: exit %d: %s", result.ExitCode, result.Stderr)
		}
		if result, err := ExecOverSSH(ctx, sess, "git -C "+ShellEscape(repoPath)+" init -q", nil, nil); err != nil {
			return fmt.Errorf("git init on sidecar: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("git init on sidecar: exit %d: %s", result.ExitCode, result.Stderr)
		}
	}
	if originURL == "" {
		return nil
	}
	// git config remote.origin.url is idempotent: creates or updates the URL.
	setURL := "git -C " + ShellEscape(repoPath) + " config remote.origin.url " + ShellEscape(originURL)
	if result, err := ExecOverSSH(ctx, sess, setURL, nil, nil); err != nil {
		return fmt.Errorf("set remote origin on sidecar: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("set remote origin on sidecar: exit %d: %s", result.ExitCode, result.Stderr)
	}
	return nil
}

// rsyncErrDetail trims ssh's stderr down to what is worth showing the user.
//
// UserKnownHostsFile=/dev/null means ssh never remembers the proxy host, so it
// announces "Warning: Permanently added ..." on every connection. Dropping it
// stops an expected notice from fronting the real cause of an rsync failure.
func rsyncErrDetail(stderr string) string {
	lines := strings.Split(stderr, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "Warning: Permanently added ") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// sshCommand builds the command rsync passes to -e in order to reach the
// sidecar through the local proxy listening on port.
//
// A user ssh_config cannot override what we pass here: command-line options are
// parsed first, so -p and every -o below win. Only options we do not set can leak
// in, and just three can redirect this hop — ProxyCommand, ProxyJump and the
// ControlMaster socket — so they are pinned individually rather than discarding
// the whole config with -F /dev/null. That keeps /etc/ssh/ssh_config and settings
// like UseKeychain intact, which a passphrase-protected key needs in order to
// authenticate instead of blocking on a prompt inside the rsync child.
//
// -q is deliberately absent so ssh diagnostics reach the rsync error.
func sshCommand(sess *Session, port string) string {
	return strings.Join([]string{"ssh", "-p", port,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ProxyCommand=none",
		"-o", "ProxyJump=none",
		"-o", "ControlPath=none",
		// rsync drives ssh over pipes; a config "RequestTTY yes" only adds a
		// "Pseudo-terminal will not be allocated" notice to the error detail.
		"-o", "RequestTTY=no",
		// IdentitiesOnly=yes keeps ssh from falling through to the default
		// ~/.ssh/id_* keys, which the sidecar has never been told about.
		"-o", "IdentitiesOnly=yes",
		// Pass the path directly — rsync tokenizes -e by whitespace and calls
		// execve, so shell quoting (ShellEscape) would embed literal quote
		// characters in the filename and cause ssh to reject it.
		"-i", sess.IdentityFile,
	}, " ")
}

// startSSHProxy starts a local TCP listener on a random port and bridges each
// incoming connection to the sidecar's WebSocket SSH tunnel. Returns the local
// address, a stop function, and a channel that receives the first proxy-level
// error (non-blocking send, capacity 1).
func startSSHProxy(ctx context.Context, sess *Session) (addr string, stop func(), proxyErr <-chan error, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, fmt.Errorf("listen: %w", err)
	}

	errCh := make(chan error, 1)
	proxyCtx, cancel := context.WithCancel(ctx)

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go bridgeConn(proxyCtx, conn, sess, errCh)
		}
	}()

	return ln.Addr().String(), func() {
		cancel()
		_ = ln.Close()
	}, errCh, nil
}

// bridgeConn forwards a TCP connection transparently to the sidecar's
// WebSocket SSH tunnel, enabling standard SSH clients (and rsync --rsh) to
// connect without WebSocket awareness. Dial failures are sent to errCh so
// callers can include them in error messages instead of seeing a silent close.
func bridgeConn(ctx context.Context, tcpConn net.Conn, sess *Session, errCh chan<- error) {
	defer func() { _ = tcpConn.Close() }()

	wsURL, _, err := toWebSocketURL(sess.URL)
	if err != nil {
		select {
		case errCh <- fmt.Errorf("build WebSocket URL: %w", err):
		default:
		}
		return
	}

	dialOpts := &websocket.DialOptions{}
	if strings.HasPrefix(wsURL, "wss://") {
		dialOpts.HTTPClient = wssHTTPClient()
	}

	wsConn, resp, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		select {
		case errCh <- fmt.Errorf("websocket connect to %s: %w", wsURL, err):
		default:
		}
		return
	}

	wsNetConn := websocket.NetConn(ctx, wsConn, websocket.MessageBinary)
	defer func() { _ = wsNetConn.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(wsNetConn, tcpConn)
	}()
	_, _ = io.Copy(tcpConn, wsNetConn)
	<-done
}
