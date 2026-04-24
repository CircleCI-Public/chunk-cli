package sidecar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/closer"
)

// ExecResult holds the output of a command executed over SSH.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ShellEscape escapes a string for safe use in a POSIX shell single-quoted context.
func ShellEscape(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

// ShellJoin joins args into a shell command string with POSIX single-quote escaping.
func ShellJoin(args []string) string {
	escaped := make([]string, len(args))
	for i, a := range args {
		escaped[i] = ShellEscape(a)
	}
	return strings.Join(escaped, " ")
}

// sshConn wraps an SSH client with optional cleanup (e.g. agent connection).
type sshConn struct {
	*ssh.Client
	cleanup func()
}

func (c *sshConn) Close() error {
	// ssh.Client.Close closes the underlying ssh.Conn, which in turn
	// closes the WebSocket transport we passed to ssh.NewClientConn.
	err := c.Client.Close()
	if c.cleanup != nil {
		c.cleanup()
	}
	// Both sides may initiate close simultaneously; if the remote end already
	// closed the connection, treat it as a successful close.
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// toWebSocketURL normalises a sidecar URL into a WebSocket URL with the
// /ssh/tunnel path appended. It returns the normalised URL string and the
// hostname (for SSH host key TOFU), avoiding a second url.Parse in the caller.
//
// The API may return bare hostnames, http://, or https:// URLs depending on
// the provider (e.g. e2b returns "https://<host>"). This converts:
//
//	http://host  →  ws://host/ssh/tunnel
//	https://host →  wss://host/ssh/tunnel
//	ws://host    →  ws://host/ssh/tunnel
//	wss://host   →  wss://host/ssh/tunnel
func toWebSocketURL(raw string) (wsURL, host string, err error) {
	// url.Parse rejects bare host:port strings (no scheme) with "first path
	// segment cannot contain colon". Prepend ws:// so it parses correctly; the
	// scheme will be overwritten below if an explicit one was already present.
	if !strings.Contains(raw, "://") {
		raw = "ws://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	if !strings.HasSuffix(u.Path, "/ssh/tunnel") {
		u.Path = strings.TrimRight(u.Path, "/") + "/ssh/tunnel"
	}
	return u.String(), u.Hostname(), nil
}

// dialSSH establishes an SSH client connection to the sidecar over a WebSocket tunnel.
// The caller must close the returned sshConn.
func dialSSH(ctx context.Context, session *Session) (*sshConn, error) {
	authMethod, cleanup, err := sshAuth(ctx, session)
	if err != nil {
		return nil, err
	}

	wsURL, host, err := toWebSocketURL(session.URL)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("build tunnel URL: %w", err)
	}

	// Dial the WebSocket tunnel. For wss:// URLs, skip TLS verification since
	// trust is established via SSH host key pinning (TOFU) below.
	dialOpts := &websocket.DialOptions{}
	if strings.HasPrefix(wsURL, "wss://") {
		dialOpts.HTTPClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, //nolint:gosec // sidecar uses self-signed certs; trust via SSH host key TOFU
				},
			},
		}
	}

	wsConn, resp, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		cleanup()
		return nil, fmt.Errorf("websocket connect to %s: %w", wsURL, err)
	}

	netConn := websocket.NetConn(ctx, wsConn, websocket.MessageBinary)
	hostKeyCallback := tofuHostKeyCallback(session.KnownHosts, host)

	conn, chans, reqs, err := ssh.NewClientConn(netConn, host, &ssh.ClientConfig{
		User:            defaultSSHUser,
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: hostKeyCallback,
	})
	if err != nil {
		_ = netConn.Close()
		cleanup()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}

	return &sshConn{Client: ssh.NewClient(conn, chans, reqs), cleanup: cleanup}, nil
}

// setSessionEnv sets environment variables on an SSH session in sorted key order.
func setSessionEnv(sess *ssh.Session, vars map[string]string) error {
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := sess.Setenv(name, vars[name]); err != nil {
			return fmt.Errorf("set env %s: %w", name, err)
		}
	}
	return nil
}

// ExecOverSSH connects to the sidecar via SSH-over-TLS and executes a command.
func ExecOverSSH(ctx context.Context, session *Session, command string, stdin io.Reader, envVars map[string]string) (_ *ExecResult, err error) {
	client, err := dialSSH(ctx, session)
	if err != nil {
		return nil, err
	}
	defer closer.ErrorHandler(client, &err)

	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ssh session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	if err := setSessionEnv(sess, envVars); err != nil {
		return nil, err
	}

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	if stdin != nil {
		sess.Stdin = stdin
	}

	exitCode := 0
	if err := sess.Run(command); err != nil {
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitStatus()
		} else {
			return nil, fmt.Errorf("ssh exec: %w", err)
		}
	}

	return &ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

// InteractiveShell opens an interactive shell session to the sidecar with PTY.
// It intentionally uses os.Stdin/os.Stdout/os.Stderr directly rather than
// iostream.Streams: term.MakeRaw and term.GetSize require a real *os.File fd,
// and PTY I/O must be wired to the process's actual terminal.
func InteractiveShell(ctx context.Context, session *Session, envVars map[string]string) (err error) {
	client, err := dialSSH(ctx, session)
	if err != nil {
		return err
	}
	defer closer.ErrorHandler(client, &err)

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer closer.ErrorHandler(sess, &err)

	// Put local terminal into raw mode so keystrokes pass through directly.
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("set terminal raw mode: %w", err)
	}
	defer func() { _ = term.Restore(fd, oldState) }()

	w, h, err := term.GetSize(fd)
	if err != nil {
		w, h = 80, 24
	}

	if err := sess.RequestPty("xterm-256color", h, w, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		return fmt.Errorf("request PTY: %w", err)
	}

	sess.Stdin = os.Stdin
	sess.Stdout = os.Stdout
	sess.Stderr = os.Stderr

	// Forward SIGWINCH to update remote terminal size.
	done := make(chan struct{})
	go watchWindowSize(fd, sess, done)
	defer close(done)

	if err := setSessionEnv(sess, envVars); err != nil {
		return err
	}

	if err := sess.Shell(); err != nil {
		return fmt.Errorf("start shell: %w", err)
	}

	return sess.Wait()
}

// sshAuth returns the appropriate SSH auth method and a cleanup function.
// The caller must call cleanup when the SSH session is done.
func sshAuth(ctx context.Context, session *Session) (ssh.AuthMethod, func(), error) {
	noop := func() {}

	if session.UseAgent {
		ag, conn, err := dialAgent(ctx, session.AuthSock)
		if err != nil {
			return nil, noop, err
		}
		return ssh.PublicKeysCallback(ag.Signers), func() { _ = conn.Close() }, nil
	}

	privateKeyData, err := os.ReadFile(session.IdentityFile)
	if err != nil {
		return nil, noop, fmt.Errorf("read private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(privateKeyData)
	if err != nil {
		return nil, noop, fmt.Errorf("parse private key: %w", err)
	}
	return ssh.PublicKeys(signer), noop, nil
}

// tofuHostKeyCallback implements trust-on-first-use host key verification.
func tofuHostKeyCallback(knownHostsPath, host string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) (err error) {
		fingerprint := sha256.Sum256(key.Marshal())
		fp := hex.EncodeToString(fingerprint[:])

		contents, err := os.ReadFile(knownHostsPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("read known hosts: %w", err)
			}
			// File doesn't exist yet — trust on first use.
			if err := os.MkdirAll(filepath.Dir(knownHostsPath), 0o700); err != nil {
				return fmt.Errorf("create known hosts dir: %w", err)
			}
			return os.WriteFile(knownHostsPath, []byte(host+" "+fp+"\n"), 0o600)
		}

		for _, line := range strings.Split(string(contents), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[0] == host {
				if parts[1] == fp {
					return nil // known and matches
				}
				return fmt.Errorf("host key mismatch for %s: expected %s, got %s", host, parts[1], fp)
			}
		}

		// New host — append and trust.
		f, err := os.OpenFile(knownHostsPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("append known hosts: %w", err)
		}
		defer closer.ErrorHandler(f, &err)
		_, err = fmt.Fprintf(f, "%s %s\n", host, fp)
		return err
	}
}
