package sandbox_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/sandbox"
)

// startWebSocketSSHServer starts a test HTTP server that upgrades connections
// to WebSocket at /ssh/tunnel and serves a minimal SSH server on each one.
//
// Returns:
//   - wsURL: the ws:// URL to dial (e.g. ws://127.0.0.1:PORT/ssh/tunnel)
//   - knownHostsPath: pre-populated known_hosts with the server's host fingerprint
//   - identityFile: client private key file the server will accept
func startWebSocketSSHServer(t *testing.T) (wsURL, knownHostsPath, identityFile string) {
	t.Helper()

	// Generate SSH host key for the server.
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	assert.NilError(t, err)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	assert.NilError(t, err)

	// Generate client keypair; the server accepts only this key.
	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	assert.NilError(t, err)
	authorizedKey, err := ssh.NewPublicKey(clientPub)
	assert.NilError(t, err)

	serverCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(authorizedKey.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unauthorized key")
		},
	}
	serverCfg.AddHostKey(hostSigner)

	mux := http.NewServeMux()
	mux.HandleFunc("/ssh/tunnel", func(w http.ResponseWriter, r *http.Request) {
		ws, wsErr := websocket.Accept(w, r, nil)
		if wsErr != nil {
			t.Logf("websocket accept: %v", wsErr)
			return
		}
		// Use context.Background() so the SSH session outlives the HTTP handler.
		// r.Context() is cancelled when the handler returns, which would prematurely
		// close the WebSocket before the SSH session completes.
		go serveSSH(t, websocket.NetConn(context.Background(), ws, websocket.MessageBinary), serverCfg)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()

	// Write client private key file.
	identityFile = filepath.Join(dir, "id_ed25519")
	privBlock, err := ssh.MarshalPrivateKey(clientPriv, "")
	assert.NilError(t, err)
	err = os.WriteFile(identityFile, pem.EncodeToMemory(privBlock), 0o600)
	assert.NilError(t, err)

	// Pre-populate known_hosts. The TOFU callback keys on the hostname from the URL.
	host := strings.SplitN(strings.TrimPrefix(srv.URL, "http://"), ":", 2)[0]
	fp := sha256.Sum256(hostSigner.PublicKey().Marshal())
	knownHostsPath = filepath.Join(dir, "known_hosts")
	err = os.WriteFile(knownHostsPath, []byte(host+" "+hex.EncodeToString(fp[:])+"\n"), 0o600)
	assert.NilError(t, err)

	wsURL = "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/ssh/tunnel"
	return wsURL, knownHostsPath, identityFile
}

// serveSSH performs the SSH server handshake on conn then dispatches session channels.
// Runs in a goroutine; it logs unexpected errors but does not call t.Fatal — the
// client-side error is what test assertions check.
func serveSSH(t *testing.T, conn net.Conn, cfg *ssh.ServerConfig) {
	t.Helper()
	defer conn.Close() //nolint:errcheck

	srvConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		t.Logf("SSH server handshake: %v", err)
		return
	}
	defer srvConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		ch, sessionReqs, acceptErr := newChan.Accept()
		if acceptErr != nil {
			t.Logf("accept session channel: %v", acceptErr)
			return
		}
		go handleSessionRequests(ch, sessionReqs)
	}
}

// handleSessionRequests handles "env" and "exec" requests on an SSH session channel.
// For exec it writes "ran: <command>\n" to stdout and sends exit-status 0.
func handleSessionRequests(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "env":
			_ = req.Reply(true, nil)
		case "exec":
			_ = req.Reply(true, nil)
			// SSH exec payload encodes the command as a uint32 length + bytes.
			cmd := ""
			if len(req.Payload) >= 4 {
				n := binary.BigEndian.Uint32(req.Payload[:4])
				if int(n) <= len(req.Payload[4:]) {
					cmd = string(req.Payload[4 : 4+n])
				}
			}
			_, _ = ch.Write([]byte("ran: " + cmd + "\n"))
			// Exit-status payload: uint32 big-endian exit code (0 = success).
			_, _ = ch.SendRequest("exit-status", false, make([]byte, 4))
			return
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// writeTestIdentityFile generates a throwaway ed25519 private key and writes it to
// a temp file, returning the path.
func writeTestIdentityFile(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	assert.NilError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	assert.NilError(t, err)
	path := filepath.Join(t.TempDir(), "id_ed25519")
	err = os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
	assert.NilError(t, err)
	return path
}

// TestExecOverSSHViaWebSocket verifies the happy path: a command executes successfully
// through a WebSocket tunnel and returns output with exit code 0.
func TestExecOverSSHViaWebSocket(t *testing.T) {
	wsURL, knownHostsPath, identityFile := startWebSocketSSHServer(t)

	session := &sandbox.Session{
		URL:          wsURL,
		IdentityFile: identityFile,
		KnownHosts:   knownHostsPath,
	}

	result, err := sandbox.ExecOverSSH(context.Background(), session, "echo hello", nil, nil)
	assert.NilError(t, err)
	assert.Equal(t, result.ExitCode, 0)
	assert.Assert(t, strings.Contains(result.Stdout, "echo hello"),
		"expected stdout to contain the command, got: %q", result.Stdout)
}

// TestExecOverSSHViaWebSocketEnvVars verifies that environment variables are forwarded
// without error.
func TestExecOverSSHViaWebSocketEnvVars(t *testing.T) {
	wsURL, knownHostsPath, identityFile := startWebSocketSSHServer(t)

	session := &sandbox.Session{
		URL:          wsURL,
		IdentityFile: identityFile,
		KnownHosts:   knownHostsPath,
	}

	result, err := sandbox.ExecOverSSH(context.Background(), session, "printenv FOO", nil,
		map[string]string{"FOO": "bar"},
	)
	assert.NilError(t, err)
	assert.Equal(t, result.ExitCode, 0)
}

// TestDialSSHWebSocketConnectionRefused verifies that a clear error is returned when
// the WebSocket server is unreachable.
func TestDialSSHWebSocketConnectionRefused(t *testing.T) {
	// Grab a port via a real server, then immediately close it so connections are refused.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	wsURL := "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/ssh/tunnel"
	srv.Close()

	session := &sandbox.Session{
		URL:          wsURL,
		IdentityFile: writeTestIdentityFile(t),
		KnownHosts:   filepath.Join(t.TempDir(), "known_hosts"),
	}

	_, err := sandbox.ExecOverSSH(context.Background(), session, "echo hi", nil, nil)
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "WebSocket connect"),
		"expected WebSocket connect error, got: %v", err)
}

// TestDialSSHWebSocketHostKeyMismatch verifies that a tampered known_hosts entry
// causes the connection to be rejected with a host key mismatch error.
func TestDialSSHWebSocketHostKeyMismatch(t *testing.T) {
	wsURL, knownHostsPath, identityFile := startWebSocketSSHServer(t)

	// Overwrite known_hosts with a wrong fingerprint.
	host := strings.SplitN(strings.TrimPrefix(wsURL, "ws://"), ":", 2)[0]
	err := os.WriteFile(knownHostsPath,
		[]byte(host+" "+strings.Repeat("aa", 32)+"\n"), 0o600)
	assert.NilError(t, err)

	session := &sandbox.Session{
		URL:          wsURL,
		IdentityFile: identityFile,
		KnownHosts:   knownHostsPath,
	}

	_, err = sandbox.ExecOverSSH(context.Background(), session, "echo hi", nil, nil)
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "host key mismatch"),
		"expected host key mismatch error, got: %v", err)
}
