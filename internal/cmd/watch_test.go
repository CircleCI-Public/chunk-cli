package cmd

import (
	"path/filepath"
	"slices"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// registerProject writes the project-root breadcrumb chunk drops for every
// project it has seen, so AllProjectRoots discovers root. It returns the root as
// AllProjectRoots reports it — canonical, symlinks resolved — which is not the
// string passed in when the path runs through one (every darwin temp dir does).
func registerProject(t *testing.T, root string) string {
	t.Helper()
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)
	assert.NilError(t, sidecar.RegisterProjectRoot(dataDir, root))
	canonical, err := filepath.EvalSymlinks(root)
	assert.NilError(t, err)
	return canonical
}

func TestWatchRootsDefaultsToAllKnownProjects(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	cwd, other := t.TempDir(), t.TempDir()
	knownRoot := registerProject(t, other)

	roots, err := watchRoots(cwd, false, nil)
	assert.NilError(t, err)
	assert.Assert(t, slices.Contains(roots, cwd), "cwd should always be watched: %v", roots)
	assert.Assert(t, slices.Contains(roots, knownRoot), "known project should be watched by default: %v", roots)
}

func TestWatchRootsFocusLimitsToCwdAndArgs(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	cwd, other, explicit := t.TempDir(), t.TempDir(), t.TempDir()
	_ = registerProject(t, other)

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
