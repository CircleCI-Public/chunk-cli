package sidecar_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// TestStartPreviewServer_Success verifies the happy path: the start command is
// sent detached (nohup'd, backgrounded, output redirected to the preview log
// path) and the port check succeeds immediately since the fake server returns
// exit code 0 for every exec.
func TestStartPreviewServer_Success(t *testing.T) {
	keyFile, pubKey := fakes.GenerateSSHKeypair(t)
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResult("", 0)

	session := &sidecar.Session{
		URL:          sshSrv.Addr(),
		IdentityFile: keyFile,
		KnownHosts:   filepath.Join(t.TempDir(), "known_hosts"),
	}

	noopStatus := iostream.StatusFunc(func(_ iostream.Level, _ string) {})

	err := sidecar.StartPreviewServer(context.Background(), session, "/home/user/repo",
		"npm run dev", 3000, map[string]string{"FOO": "bar"}, noopStatus)
	assert.NilError(t, err)

	commands := sshSrv.Commands()
	assert.Assert(t, len(commands) >= 1)
	startCmd := commands[0]
	assert.Assert(t, strings.Contains(startCmd, "nohup"), "expected nohup in: %s", startCmd)
	assert.Assert(t, strings.Contains(startCmd, "npm run dev"), "expected start command in: %s", startCmd)
	assert.Assert(t, strings.Contains(startCmd, sidecar.PreviewLogPath), "expected log redirect in: %s", startCmd)
	assert.Assert(t, strings.Contains(startCmd, "/home/user/repo"), "expected workspace cd in: %s", startCmd)
}

// TestStartPreviewServer_StartCommandFails verifies a non-zero exit from the
// start command is surfaced as an error before any port polling happens.
func TestStartPreviewServer_StartCommandFails(t *testing.T) {
	keyFile, pubKey := fakes.GenerateSSHKeypair(t)
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResult("boom", 1)

	session := &sidecar.Session{
		URL:          sshSrv.Addr(),
		IdentityFile: keyFile,
		KnownHosts:   filepath.Join(t.TempDir(), "known_hosts"),
	}

	noopStatus := iostream.StatusFunc(func(_ iostream.Level, _ string) {})

	err := sidecar.StartPreviewServer(context.Background(), session, "", "npm run dev", 3000, nil, noopStatus)
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "start preview server"))
}
