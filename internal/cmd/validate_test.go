package cmd

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// setupSSHSession starts fake CCI + SSH servers and sets env vars so
// ensureCircleCIClient resolves to the fake. Returns the SSH server and the
// identity key file path.
func setupSSHSession(t *testing.T) (*fakes.SSHServer, string) {
	t.Helper()
	isolateConfig(t)

	keyFile, pubKey := fakes.GenerateSSHKeypair(t)
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResult("hello\n", 0)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	cciSrv := httptest.NewServer(cci)
	t.Cleanup(cciSrv.Close)

	t.Setenv(config.EnvCircleToken, "test-token")
	t.Setenv(config.EnvCircleCIBaseURL, cciSrv.URL)

	return sshSrv, keyFile
}

func TestOpenSSHSessionPassesEnvVars(t *testing.T) {
	sshSrv, keyFile := setupSSHSession(t)

	envVars := map[string]string{"FOO": "bar", "BAZ": "qux"}
	execFn, _, err := openSSHSession(context.Background(), "sidecar-123", keyFile, "", envVars, discardStreams())
	assert.NilError(t, err)

	_, _, _, err = execFn(context.Background(), "echo hello")
	assert.NilError(t, err)

	got := sshSrv.EnvVars()
	assert.Equal(t, got["FOO"], "bar")
	assert.Equal(t, got["BAZ"], "qux")
}

func TestValidateEnvFlagBadValue(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()

	// Write a minimal project config so validate doesn't fail with "no commands".
	cfgDir := filepath.Join(dir, ".chunk")
	assert.NilError(t, os.MkdirAll(cfgDir, 0o755))
	assert.NilError(t, os.WriteFile(
		filepath.Join(cfgDir, "config.json"),
		[]byte(`{"commands":[{"name":"test","run":"true"}]}`),
		0o644,
	))

	cmd := newValidateCmd()
	cmd.SetOut(os.Stderr)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs([]string{"--project", dir, "--env", "BADVALUE"})

	err := cmd.Execute()
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "BADVALUE"), "got: %v", err)
}
