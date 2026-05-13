package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func runAuthCheckCmd(t *testing.T) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := NewRootCmd("test")
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"auth", "check"})
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestAuthCheckMissingToken(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	_, stderr, err := runAuthCheckCmd(t)

	assert.Assert(t, err != nil)
	var ec interface{ ExitCode() int }
	assert.Assert(t, errors.As(err, &ec), "expected ExitCode error, got %T: %v", err, err)
	assert.Equal(t, ec.ExitCode(), 1)
	assert.Assert(t, strings.Contains(stderr, "chunk auth set circleci"),
		"expected auth hint in stderr, got: %q", stderr)
}

func TestAuthCheckTokenPresentViaEnv(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "fake-token-for-test")

	stdout, stderr, err := runAuthCheckCmd(t)

	assert.NilError(t, err)
	assert.Equal(t, stdout, "")
	assert.Equal(t, stderr, "")
}

func TestAuthCheckTokenPresentViaConfigFile(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	cfg := config.UserConfig{CircleCIToken: "file-token"}
	assert.NilError(t, config.Save(cfg))

	_, _, err := runAuthCheckCmd(t)
	assert.NilError(t, err)
}

// TestAuthCheckRegistered verifies the subcommand appears under "auth".
func TestAuthCheckRegistered(t *testing.T) {
	root := NewRootCmd("test")
	var found *cobra.Command
	for _, sub := range root.Commands() {
		if sub.Use == "auth" {
			for _, child := range sub.Commands() {
				if child.Use == "check" {
					found = child
				}
			}
		}
	}
	assert.Assert(t, found != nil, "expected 'auth check' subcommand to be registered")
}
