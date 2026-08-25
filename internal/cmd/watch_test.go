package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// registerProject writes the project-root breadcrumb chunk drops for every
// project it has seen, so AllProjectRoots discovers root.
func registerProject(t *testing.T, root string) {
	t.Helper()
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)
	assert.NilError(t, os.MkdirAll(dataDir, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dataDir, "project-root"), []byte(root), 0o644))
}

func TestWatchRootsDefaultsToAllKnownProjects(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	cwd, other := t.TempDir(), t.TempDir()
	registerProject(t, other)

	roots, err := watchRoots(cwd, false, nil)
	assert.NilError(t, err)
	assert.Assert(t, slices.Contains(roots, cwd), "cwd should always be watched: %v", roots)
	assert.Assert(t, slices.Contains(roots, other), "known project should be watched by default: %v", roots)
}

func TestWatchRootsFocusLimitsToCwdAndArgs(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	cwd, other, explicit := t.TempDir(), t.TempDir(), t.TempDir()
	registerProject(t, other)

	roots, err := watchRoots(cwd, true, []string{explicit})
	assert.NilError(t, err)
	assert.DeepEqual(t, roots, []string{cwd, explicit})
}

func TestWatchAllFlagIsDeprecatedButAccepted(t *testing.T) {
	cmd := newWatchCmd()

	focus := cmd.Flags().Lookup("focus")
	assert.Assert(t, focus != nil, "--focus should be registered")
	assert.Equal(t, focus.DefValue, "false")

	all := cmd.Flags().Lookup("all")
	assert.Assert(t, all != nil, "--all should still parse for existing invocations")
	assert.Assert(t, all.Deprecated != "", "--all should be marked deprecated")
}
