package sidecar_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// TestExecOverSSHViaWebSocket verifies the happy path: a command executes
// successfully through a WebSocket tunnel and returns output with exit code 0.
func TestExecOverSSHViaWebSocket(t *testing.T) {
	keyFile, pubKey := fakes.GenerateSSHKeypair(t)
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResult("hello from sidecar\n", 0)

	session := &sidecar.Session{
		URL:          sshSrv.Addr(),
		IdentityFile: keyFile,
		KnownHosts:   filepath.Join(t.TempDir(), "known_hosts"),
	}

	result, err := sidecar.ExecOverSSH(context.Background(), session, "echo hello", nil, nil)
	assert.NilError(t, err)
	assert.Equal(t, result.ExitCode, 0)
	assert.Equal(t, result.Stdout, "hello from sidecar\n")
}

// TestExecOverSSHViaWebSocketEnvVars verifies that environment variables are
// forwarded to the session without error.
func TestExecOverSSHViaWebSocketEnvVars(t *testing.T) {
	keyFile, pubKey := fakes.GenerateSSHKeypair(t)
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResult("bar\n", 0)

	session := &sidecar.Session{
		URL:          sshSrv.Addr(),
		IdentityFile: keyFile,
		KnownHosts:   filepath.Join(t.TempDir(), "known_hosts"),
	}

	result, err := sidecar.ExecOverSSH(context.Background(), session, "printenv FOO", nil,
		map[string]string{"FOO": "bar"},
	)
	assert.NilError(t, err)
	assert.Equal(t, result.ExitCode, 0)
}

// TestDialSSHWebSocketConnectionRefused verifies that a clear error is returned
// when the WebSocket server is unreachable.
func TestDialSSHWebSocketConnectionRefused(t *testing.T) {
	keyFile, _ := fakes.GenerateSSHKeypair(t)

	// Grab a port via a real server, then immediately close it so connections
	// to that address are refused.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/ssh/tunnel"
	srv.Close()

	session := &sidecar.Session{
		URL:          closedURL,
		IdentityFile: keyFile,
		KnownHosts:   filepath.Join(t.TempDir(), "known_hosts"),
	}

	_, err := sidecar.ExecOverSSH(context.Background(), session, "echo hi", nil, nil)
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "websocket connect"),
		"expected websocket connect error, got: %v", err)
}

// TestDialSSHWebSocketHostKeyMismatch verifies that a tampered known_hosts
// entry causes the connection to be rejected with a host key mismatch error.
func TestDialSSHWebSocketHostKeyMismatch(t *testing.T) {
	keyFile, pubKey := fakes.GenerateSSHKeypair(t)
	sshSrv := fakes.NewSSHServer(t, pubKey)

	// Write a wrong fingerprint for the server's host before the first connect.
	host := strings.SplitN(sshSrv.Addr(), ":", 2)[0]
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	err := os.WriteFile(knownHosts, []byte(host+" "+strings.Repeat("aa", 32)+"\n"), 0o600)
	assert.NilError(t, err)

	session := &sidecar.Session{
		URL:          sshSrv.Addr(),
		IdentityFile: keyFile,
		KnownHosts:   knownHosts,
	}

	_, err = sidecar.ExecOverSSH(context.Background(), session, "echo hi", nil, nil)
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "host key mismatch"),
		"expected host key mismatch error, got: %v", err)
}
