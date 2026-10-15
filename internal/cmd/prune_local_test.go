package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func runPruneLocalCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := newTestRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"prune", "--local"}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestPruneLocalDryRunListsWithoutDeleting(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv(config.EnvHome, t.TempDir())

	home, err := os.UserHomeDir()
	assert.NilError(t, err)
	factoryDir := filepath.Join(home, ".chunk", "factory")
	assert.NilError(t, os.MkdirAll(factoryDir, 0o700))

	out, _, err := runPruneLocalCmd(t)
	assert.NilError(t, err)
	assert.Assert(t, bytes.Contains([]byte(out), []byte(factoryDir)))

	_, statErr := os.Stat(factoryDir)
	assert.NilError(t, statErr, "dry run must not delete anything")
}

func TestPruneLocalYesDeletes(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv(config.EnvHome, t.TempDir())

	home, err := os.UserHomeDir()
	assert.NilError(t, err)
	factoryDir := filepath.Join(home, ".chunk", "factory")
	assert.NilError(t, os.MkdirAll(factoryDir, 0o700))

	_, _, err = runPruneLocalCmd(t, "--yes")
	assert.NilError(t, err)

	_, statErr := os.Stat(factoryDir)
	assert.Assert(t, os.IsNotExist(statErr), "--yes must delete the leftover")
}
