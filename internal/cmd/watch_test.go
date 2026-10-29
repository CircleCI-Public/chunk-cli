package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// watchProject is the git top level chunk watch was opened under, canonical
// and registered for the daemon to find, and nothing outside a repository.
func TestWatchProject(t *testing.T) {
	repo := gitRepo(t, "")
	sub := filepath.Join(repo, "sub")
	assert.NilError(t, os.MkdirAll(sub, 0o755))
	// Reached through a symlink, as every darwin temp dir is.
	link := filepath.Join(t.TempDir(), "link")
	assert.NilError(t, os.Symlink(repo, link))
	canonical := config.CanonicalProjectRoot(repo)

	tests := []struct {
		name     string
		cwd      string
		wantRoot string
		wantOK   bool
	}{
		{name: "repo root", cwd: repo, wantRoot: canonical, wantOK: true},
		{name: "repo subdirectory", cwd: sub, wantRoot: canonical, wantOK: true},
		{name: "repo through a symlink", cwd: link, wantRoot: canonical, wantOK: true},
		{name: "not a repo", cwd: t.TempDir()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateConfig(t)
			t.Setenv(config.EnvXDGDataHome, t.TempDir())

			root, ok := watchProject(t.Context(), tt.cwd)
			assert.Check(t, cmp.Equal(ok, tt.wantOK))
			assert.Check(t, cmp.Equal(root, tt.wantRoot))

			known, err := sidecar.AllProjectRoots()
			assert.NilError(t, err)
			if !tt.wantOK {
				assert.Check(t, cmp.Len(known, 0), "registered %v outside a repository", known)
				return
			}
			dataDir, err := config.ProjectDataDir(root)
			assert.NilError(t, err)
			crumb, err := os.ReadFile(sidecar.ProjectRootPath(dataDir))
			assert.NilError(t, err, "no project-root breadcrumb")
			assert.Check(t, cmp.Equal(string(crumb), canonical))
			assert.Check(t, cmp.DeepEqual(known, []string{canonical}))
		})
	}
}

func TestWatchFlagsAreDeprecatedButAccepted(t *testing.T) {
	cmd := newWatchCmd()

	focus := cmd.Flags().Lookup("focus")
	assert.Assert(t, focus != nil, "--focus should still parse for existing invocations")
	assert.Equal(t, focus.DefValue, "false")
	assert.Assert(t, focus.Deprecated != "", "--focus should be marked deprecated")

	all := cmd.Flags().Lookup("all")
	assert.Assert(t, all != nil, "--all should still parse for existing invocations")
	assert.Assert(t, all.Deprecated != "", "--all should be marked deprecated")
}

func TestWatchTakesNoArgs(t *testing.T) {
	cmd := newWatchCmd()
	assert.NilError(t, cmd.Args(cmd, nil))

	err := cmd.Args(cmd, []string{"x"})
	ue, ok := errors.AsType[*userError](err)
	assert.Assert(t, ok, "not a user error: %v", err)
	assert.Check(t, cmp.Equal(ue.UserExitCode(), ExitBadArgs))
}

func TestOpenFlows(t *testing.T) {
	flows := []circleci.Flow{
		{ID: "a", Phase: circleci.PhaseStarted, SessionID: "s1", Task: &circleci.FlowTask{ID: "t", Category: "build-unit"}},
		{ID: "b", Phase: circleci.PhaseReceived},
		{ID: "ended", State: "Done", Phase: circleci.PhaseEnded},
		{ID: "old", State: "Done"}, // a conductor that serves no phase
	}

	got := openFlows(flows)

	assert.Check(t, cmp.DeepEqual(got, []circleci.Flow{flows[0], flows[1], flows[3]}))
}

func TestPlanParents(t *testing.T) {
	plans := []circleci.Plan{{ID: "p1"}, {ID: "c1", Parent: "p1"}, {ID: "c2", Parent: "p1"}}

	got := planParents(plans)

	assert.Check(t, cmp.DeepEqual(got, map[string]string{"c1": "p1", "c2": "p1"}))
}
