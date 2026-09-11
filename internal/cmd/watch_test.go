package cmd

import (
	"os"
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

// --focus sends these roots to the daemon as a filter, and the daemon keys
// projects by the canonical spelling, so a root that reaches it unresolved
// matches nothing and the dashboard comes back empty. Git answers with a
// resolved path on darwin, which would hide this on the platform it was
// developed on.
func TestWatchRootsAreCanonicalForTheDaemonFilter(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	base := t.TempDir()
	target := filepath.Join(base, "project")
	assert.NilError(t, os.MkdirAll(target, 0o755))
	link := filepath.Join(base, "link")
	assert.NilError(t, os.Symlink(target, link))

	canonical := config.CanonicalProjectRoot(target)
	assert.Equal(t, config.CanonicalProjectRoot(link), canonical,
		"both spellings must canonicalise to one root")

	// What registration writes, and so what the daemon reports, for either.
	dataDir, err := config.ProjectDataDir(link)
	assert.NilError(t, err)
	assert.NilError(t, sidecar.RegisterProjectRoot(dataDir, link))
	roots, err := sidecar.AllProjectRoots()
	assert.NilError(t, err)
	assert.DeepEqual(t, roots, []string{canonical})
}
