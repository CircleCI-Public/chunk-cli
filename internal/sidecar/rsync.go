package sidecar

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/coder/websocket"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// RsyncSync syncs the local working tree (rooted at cwd) to a sidecar using
// rsync over an SSH-over-WebSocket tunnel. Files matching .gitignore rules are
// excluded. In a normal checkout, .git is copied so the remote has a fully
// functional git repo. In a git worktree, .git is a local pointer file that
// would be broken on the sidecar, so it is excluded and a fresh git repo is
// initialised on the remote instead. workdir overrides the destination path;
// defaults to /home/user/<repo name from git remote>.
func RsyncSync(ctx context.Context,
	client *circleci.Client, sidecarID, identityFile, authSock, workdir, cwd string,
	status iostream.StatusFunc) error {

	return rsyncTo(ctx, client, sidecarID, identityFile, authSock, workdir, cwd, true, status)
}

// RsyncSyncEphemeral syncs like RsyncSync but neither reads nor writes the
// active sidecar file. workdir is required.
func RsyncSyncEphemeral(ctx context.Context,
	client *circleci.Client, sidecarID, identityFile, authSock, workdir, cwd string,
	status iostream.StatusFunc) error {

	if workdir == "" {
		return fmt.Errorf("rsync: workdir is required for an ephemeral sync")
	}
	return rsyncTo(ctx, client, sidecarID, identityFile, authSock, workdir, cwd, false, status)
}

func rsyncTo(ctx context.Context, client *circleci.Client,
	sidecarID, identityFile, authSock, workdir, cwd string, persist bool,
	status iostream.StatusFunc) error {

	sess, err := OpenSession(ctx, client, sidecarID, identityFile, authSock, false)
	if err != nil {
		return fmt.Errorf("rsync: open session: %w", err)
	}

	// Detect a git worktree early: in a worktree .git is a file (not a dir)
	// containing a gitdir pointer to an absolute local path. This affects both
	// workspace resolution (use repo name, not directory name) and how the
	// remote git repo is set up after the sync.
	gitInfo, statErr := os.Stat(filepath.Join(cwd, ".git"))
	isWorktree := statErr == nil && !gitInfo.IsDir()

	// In a worktree, fetch the origin URL once; it is used for workspace
	// resolution (to parse the repo name) and to configure the remote on the
	// sidecar after init.
	var worktreeOriginURL string
	if isWorktree {
		if out, execErr := exec.CommandContext(ctx, "git", "-C", cwd, "remote", "get-url", "origin").Output(); execErr == nil {
			worktreeOriginURL = strings.TrimSpace(string(out))
		}
	}

	repoPath := workdir
	if persist {
		if isWorktree && worktreeOriginURL != "" && workdir == "" {
			// Skip the saved workspace in a worktree: it may have been recorded as
			// the worktree directory name by older code. Derive from the repo name
			// so the path matches what every other code path expects.
			_, repo, _ := gitremote.ParseRemoteURL(worktreeOriginURL)
			repoPath = DefaultWorkspace(repo)
		} else {
			_, repo, repoErr := gitremote.DetectOrgAndRepo(cwd)
			if repoErr != nil {
				repo = filepath.Base(cwd)
			}
			repoPath, err = ResolveWorkspace(ctx, workdir, repo)
			if err != nil {
				return fmt.Errorf("rsync: resolve workspace: %w", err)
			}
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

	localAddr, stopProxy, err := startSSHProxy(ctx, sess)
	if err != nil {
		return fmt.Errorf("rsync: start SSH proxy: %w", err)
	}
	defer stopProxy()

	_, port, err := net.SplitHostPort(localAddr)
	if err != nil {
		return fmt.Errorf("rsync: parse proxy addr: %w", err)
	}

	sshArgs := []string{"ssh", "-p", port,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes", // prevent agent key flood before explicit key is tried
		"-q",
	}
	if sess.IdentityFile != "" {
		// Pass path directly — rsync tokenizes -e by whitespace and calls execve,
		// so shell quoting (ShellEscape) would embed literal quote characters in
		// the filename and cause ssh to fall through to agent keys.
		sshArgs = append(sshArgs, "-i", sess.IdentityFile)
	} else if sess.UseAgent && sess.AuthSock != "" {
		sshArgs = append(sshArgs, "-o", "IdentitiesOnly=no")
	}
	sshCmd := strings.Join(sshArgs, " ")

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
		rsyncArgs = append(rsyncArgs, "--exclude=.git")
	}
	rsyncArgs = append(rsyncArgs, "-e", sshCmd, src, dst)

	cmd := exec.CommandContext(ctx, "rsync", rsyncArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("rsync: %w\n%s", err, detail)
		}
		return fmt.Errorf("rsync: %w", err)
	}

	if isWorktree {
		if err := initWorktreeGitRepo(ctx, sess, repoPath, worktreeOriginURL); err != nil {
			return fmt.Errorf("rsync: %w", err)
		}
	}

	status(iostream.LevelDone, "Synced")
	return nil
}

// initWorktreeGitRepo sets up a minimal git repo in repoPath on the sidecar
// after an rsync that excluded the .git pointer file. It removes any stale
// .git entry, runs git init, and wires up the origin URL so that git remote
// detection works inside the workspace.
func initWorktreeGitRepo(ctx context.Context, sess *Session, repoPath, originURL string) error {
	// Remove any stale .git entry left by a previous sync (broken worktree
	// pointer file or directory from a prior git init).
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

// startSSHProxy starts a local TCP listener on a random port and bridges each
// incoming connection to the sidecar's WebSocket SSH tunnel. Returns the local
// address and a stop function.
func startSSHProxy(ctx context.Context, sess *Session) (addr string, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("listen: %w", err)
	}

	proxyCtx, cancel := context.WithCancel(ctx)

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go bridgeConn(proxyCtx, conn, sess)
		}
	}()

	return ln.Addr().String(), func() {
		cancel()
		_ = ln.Close()
	}, nil
}

// bridgeConn forwards a TCP connection transparently to the sidecar's
// WebSocket SSH tunnel, enabling standard SSH clients (and rsync --rsh) to
// connect without WebSocket awareness.
func bridgeConn(ctx context.Context, tcpConn net.Conn, sess *Session) {
	defer func() { _ = tcpConn.Close() }()

	wsURL, _, err := toWebSocketURL(sess.URL)
	if err != nil {
		return
	}

	dialOpts := &websocket.DialOptions{}
	if strings.HasPrefix(wsURL, "wss://") {
		dialOpts.HTTPClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // trust via SSH key, not TLS cert
			},
		}
	}

	wsConn, resp, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
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
