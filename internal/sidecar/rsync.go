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
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("rsync: %w\n%s", err, detail)
		}
		return fmt.Errorf("rsync: %w", err)
	}

	status(iostream.LevelDone, "Synced")
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
