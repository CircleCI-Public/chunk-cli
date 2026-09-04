package sidecar

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/coder/websocket"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// RsyncSync syncs the local working tree (rooted at cwd) to a sidecar using
// rsync over an SSH-over-WebSocket tunnel. The .git directory is included so
// the remote has a fully functional git repo; files matching .gitignore rules
// are excluded. workdir overrides the destination path; defaults to
// /home/user/<basename of cwd>.
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

	sess, err := OpenSession(ctx, client, sidecarID, identityFile, authSock)
	if err != nil {
		return fmt.Errorf("rsync: open session: %w", err)
	}

	repoPath := workdir
	if persist {
		repo := filepath.Base(cwd)
		repoPath, err = ResolveWorkspace(ctx, workdir, repo)
		if err != nil {
			return fmt.Errorf("rsync: resolve workspace: %w", err)
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

	sshCmd := sshCommand(sess, port)

	src := strings.TrimRight(cwd, "/") + "/"
	dst := fmt.Sprintf("%s@127.0.0.1:%s", defaultSSHUser, repoPath)

	cmd := exec.CommandContext(ctx, "rsync",
		"--archive",
		"--delete",
		"--filter=:- .gitignore",
		"-e", sshCmd,
		src, dst,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := rsyncErrDetail(stderr.String())
		if detail != "" {
			return fmt.Errorf("rsync: %w\n%s", err, detail)
		}
		return fmt.Errorf("rsync: %w", err)
	}

	status(iostream.LevelDone, "Synced")
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
// IdentitiesOnly=yes is set only alongside an explicit -i. OpenSSH honours the
// first occurrence of an option, so setting it unconditionally cannot be undone
// later: an IdentitiesOnly=no appended for agent sessions is silently ignored,
// leaving ssh unable to offer the agent key OpenSession registered. With no -i
// to restrict, IdentitiesOnly=yes also does not mean "no keys" — ssh falls back
// to the default ~/.ssh/id_* filenames, so it offers unrelated keys instead.
//
// A user ssh_config cannot override what we pass here: command-line options are
// parsed first, so -p and every -o below win. Only options we do not set can leak
// in, and just three can redirect this hop — ProxyCommand, ProxyJump and the
// ControlMaster socket — so they are pinned individually rather than discarding
// the whole config with -F /dev/null. That keeps /etc/ssh/ssh_config and settings
// like UseKeychain intact, which a passphrase-protected -i key needs in order to
// authenticate instead of blocking on a prompt inside the rsync child.
//
// -q is deliberately absent so ssh diagnostics reach the rsync error.
func sshCommand(sess *Session, port string) string {
	args := []string{"ssh", "-p", port,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ProxyCommand=none",
		"-o", "ProxyJump=none",
		"-o", "ControlPath=none",
		// rsync drives ssh over pipes; a config "RequestTTY yes" only adds a
		// "Pseudo-terminal will not be allocated" notice to the error detail.
		"-o", "RequestTTY=no",
	}
	if sess.IdentityFile != "" {
		// Pass path directly — rsync tokenizes -e by whitespace and calls execve,
		// so shell quoting (ShellEscape) would embed literal quote characters in
		// the filename and cause ssh to fall through to agent keys.
		args = append(args, "-o", "IdentitiesOnly=yes", "-i", sess.IdentityFile)
	} else if sess.UseAgent && sess.AuthSock != "" {
		// Set IdentitiesOnly=no explicitly: a user ssh_config with
		// "IdentitiesOnly yes" would otherwise stop ssh offering the agent key
		// OpenSession registered, reproducing the failure this helper fixes.
		// Pinning IdentityAgent keeps the agent in step with ExecOverSSH, which
		// dials sess.AuthSock rather than the ambient SSH_AUTH_SOCK.
		args = append(args, "-o", "IdentitiesOnly=no", "-o", "IdentityAgent="+sess.AuthSock)
	}
	return strings.Join(args, " ")
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
