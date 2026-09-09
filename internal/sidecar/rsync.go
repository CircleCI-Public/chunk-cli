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
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// RsyncSync syncs the local working tree (rooted at cwd) to a sidecar using
// rsync over an SSH-over-WebSocket tunnel. The .git directory is included so
// the remote has a fully functional git repo; files matching .gitignore rules
// are excluded. workdir overrides the destination path; defaults to
// /home/user/<basename of cwd>.
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

	keyPath, err := DefaultKeyPath()
	if err != nil {
		return fmt.Errorf("rsync: resolve key path: %w", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("rsync: stat SSH key: %w", err)
		}
		if err := GenerateKeyPair(keyPath); err != nil {
			return fmt.Errorf("rsync: generate SSH key: %w", err)
		}
	}

	sess, err := OpenSession(ctx, client, sidecarID, false)
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

	localAddr, stopProxy, proxyErr, err := startSSHProxy(ctx, sess)
	if err != nil {
		return fmt.Errorf("rsync: start SSH proxy: %w", err)
	}
	defer stopProxy()

	_, port, err := net.SplitHostPort(localAddr)
	if err != nil {
		return fmt.Errorf("rsync: parse proxy addr: %w", err)
	}

	// Pass the key path directly without shell quoting — rsync tokenizes -e by
	// whitespace and calls execve, so ShellEscape would embed literal quotes in
	// the filename and cause SSH to reject it.
	sshCmd := strings.Join([]string{
		"ssh", "-p", port,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-i", keyPath,
		"-q",
	}, " ")

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
		var proxyDetail string
		select {
		case pe := <-proxyErr:
			proxyDetail = pe.Error()
		default:
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

	status(iostream.LevelDone, "Synced")
	return nil
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
