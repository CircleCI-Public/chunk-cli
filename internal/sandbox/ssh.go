package sandbox

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
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

// ExecOverSSH connects to the sandbox via SSH-over-TLS and executes a command.
func ExecOverSSH(session *Session, command string, stdin io.Reader) (*ExecResult, error) {
	privateKeyData, err := os.ReadFile(session.IdentityFile)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(privateKeyData)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	// TLS dial — self-signed cert on sandbox hosts, so skip verification.
	// Trust is established via SSH host key pinning (TOFU) below.
	tlsConn, err := tls.Dial("tcp", net.JoinHostPort(session.URL, fmt.Sprintf("%d", defaultSSHPort)), &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // sandbox uses self-signed certs; trust via SSH host key TOFU
	})
	if err != nil {
		return nil, fmt.Errorf("TLS connect to %s:%d: %w", session.URL, defaultSSHPort, err)
	}
	defer func() { _ = tlsConn.Close() }()

	hostKeyCallback := tofuHostKeyCallback(session.KnownHosts, session.URL)

	conn, chans, reqs, err := ssh.NewClientConn(tlsConn, session.URL, &ssh.ClientConfig{
		User:            defaultSSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
	})
	if err != nil {
		return nil, fmt.Errorf("SSH handshake: %w", err)
	}
	defer func() { _ = conn.Close() }()

	client := ssh.NewClient(conn, chans, reqs)
	defer func() { _ = client.Close() }()

	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("SSH session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	if stdin != nil {
		sess.Stdin = stdin
	}

	exitCode := 0
	if err := sess.Run(command); err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
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

// tofuHostKeyCallback implements trust-on-first-use host key verification.
func tofuHostKeyCallback(knownHostsPath, host string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
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
		defer func() { _ = f.Close() }()
		_, err = fmt.Fprintf(f, "%s %s\n", host, fp)
		return err
	}
}
