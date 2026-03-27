package sandbox

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
	"os"
	"path/filepath"
	"strings"

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
	// closes the TLS transport we passed to ssh.NewClientConn.
	err := c.Client.Close()
	if c.cleanup != nil {
		c.cleanup()
	}
	return err
}

// dialSSH establishes an SSH client connection to the sandbox over TLS.
// The caller must close the returned sshConn.
func dialSSH(ctx context.Context, session *Session) (*sshConn, error) {
	authMethod, cleanup, err := sshAuth(ctx, session)
	if err != nil {
		return nil, err
	}

	// TLS dial — self-signed cert on sandbox hosts, so skip verification.
	// Trust is established via SSH host key pinning (TOFU) below.
	tlsConn, err := tls.Dial("tcp", net.JoinHostPort(session.URL, fmt.Sprintf("%d", defaultSSHPort)), &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // sandbox uses self-signed certs; trust via SSH host key TOFU
	})
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("TLS connect to %s:%d: %w", session.URL, defaultSSHPort, err)
	}

	hostKeyCallback := tofuHostKeyCallback(session.KnownHosts, session.URL)

	conn, chans, reqs, err := ssh.NewClientConn(tlsConn, session.URL, &ssh.ClientConfig{
		User:            defaultSSHUser,
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: hostKeyCallback,
	})
	if err != nil {
		_ = tlsConn.Close()
		cleanup()
		return nil, fmt.Errorf("SSH handshake: %w", err)
	}

	return &sshConn{Client: ssh.NewClient(conn, chans, reqs), cleanup: cleanup}, nil
}

// ExecOverSSH connects to the sandbox via SSH-over-TLS and executes a command.
func ExecOverSSH(ctx context.Context, session *Session, command string, stdin io.Reader) (_ *ExecResult, err error) {
	client, err := dialSSH(ctx, session)
	if err != nil {
		return nil, err
	}
	defer closer.ErrorHandler(client, &err)

	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("SSH session: %w", err)
	}
	defer closer.ErrorHandler(sess, &err)

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
			return nil, fmt.Errorf("SSH exec: %w", err)
		}
	}

	return &ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

// InteractiveShell opens an interactive shell session to the sandbox with PTY.
// It intentionally uses os.Stdin/os.Stdout/os.Stderr directly rather than
// iostream.Streams: term.MakeRaw and term.GetSize require a real *os.File fd,
// and PTY I/O must be wired to the process's actual terminal.
func InteractiveShell(ctx context.Context, session *Session) (err error) {
	client, err := dialSSH(ctx, session)
	if err != nil {
		return err
	}
	defer closer.ErrorHandler(client, &err)

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("SSH session: %w", err)
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
