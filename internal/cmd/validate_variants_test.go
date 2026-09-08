package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
	"github.com/CircleCI-Public/chunk-cli/internal/variants"
)

// isolateSidecarState points the sidecar state directory at a temp dir so
// resolveVariantsWorkspace sees no active sidecar and has to fall back to the
// repo-derived default.
func isolateSidecarState(t *testing.T) {
	t.Helper()
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, filepath.Join(t.TempDir(), "data"))
}

// TestResolveVariantsWorkspaceDefaultsToSidecarHome is the regression guard for a
// default that used to be ./workspace/<repo>. `chunk sidecar env build` provisions
// dependencies in <sidecarHome>/<repo> before the snapshot is taken, so syncing
// anywhere else hands every variant a tree the snapshot never prepared: the
// commands fail environmentally and every mutant reads as caught.
func TestResolveVariantsWorkspaceDefaultsToSidecarHome(t *testing.T) {
	isolateSidecarState(t)
	t.Setenv("CHUNK_SIDECAR_HOME", "/home/runner")

	repoDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")

	ws, err := resolveVariantsWorkspace(context.Background(), "", repoDir)
	assert.NilError(t, err)
	// workspace uses the local directory basename, not the git remote repo name
	assert.Equal(t, ws, "/home/runner/"+filepath.Base(repoDir))
}

func TestResolveVariantsWorkspacePrefersWorkdirFlag(t *testing.T) {
	isolateSidecarState(t)

	// No git repo and no active sidecar: the flag is the only source, and it wins
	// before either is consulted.
	ws, err := resolveVariantsWorkspace(context.Background(), "/somewhere/else", t.TempDir())
	assert.NilError(t, err)
	assert.Equal(t, ws, "/somewhere/else")
}

// TestResolveVariantsWorkspaceFallsBackToBasename verifies that without a git
// remote or active sidecar, the workspace defaults to <sidecarHome>/<basename>.
func TestResolveVariantsWorkspaceFallsBackToBasename(t *testing.T) {
	isolateSidecarState(t)
	t.Setenv("CHUNK_SIDECAR_HOME", "/home/user")

	dir := t.TempDir()
	ws, err := resolveVariantsWorkspace(context.Background(), "", dir)
	assert.NilError(t, err)
	assert.Equal(t, ws, "/home/user/"+filepath.Base(dir))
}

func TestCommandTimeout(t *testing.T) {
	// A command's own timeout wins.
	assert.Equal(t, commandTimeout(90, 300), 90)
	// Otherwise the run-wide default applies.
	assert.Equal(t, commandTimeout(0, 300), 300)
	// A run-wide default of 0 means no limit, and must not be raised to one.
	assert.Equal(t, commandTimeout(0, 0), 0)
}

// TestVariantCommandsExpandsTemplates guards the gap that let a literal
// {{CHANGED_PACKAGES}} reach the sidecar's shell, where it exits non-zero for a
// shell reason and reads as a caught mutant for every variant in the run.
func TestVariantCommandsExpandsTemplates(t *testing.T) {
	repoDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")

	got := variantCommands([]config.Command{
		{Name: "test-changed", Run: "task test -- {{CHANGED_PACKAGES}}", Remote: true},
	}, repoDir, 300)

	assert.Equal(t, len(got), 1)
	assert.Equal(t, got[0].Name, "test-changed")
	assert.Equal(t, got[0].Timeout, 300)
	assert.Assert(t, !strings.Contains(got[0].Run, "{{CHANGED_PACKAGES}}"),
		"template must be expanded before it is shipped, got %q", got[0].Run)
}

func captureStatus() (iostream.StatusFunc, func() string) {
	var msgs []string
	return func(_ iostream.Level, msg string) { msgs = append(msgs, msg) },
		func() string { return strings.Join(msgs, "\n") }
}

// TestReportVariantSummaryWarnsWhenNothingSurvived covers the last line of defence
// against a broken run reading as a clean bill of health: a snapshot missing the
// project's tooling fails identically for every variant, which looks exactly like
// perfect coverage in the per-variant JSON.
func TestReportVariantSummaryWarnsWhenNothingSurvived(t *testing.T) {
	status, dump := captureStatus()

	reportVariantSummary([]variants.Result{
		{ID: "MUT-001", Killed: true, ExitCode: 1},
		{ID: "MUT-002", Killed: true, ExitCode: 1},
	}, status)

	out := dump()
	assert.Assert(t, strings.Contains(out, "2/2 variants killed"), "got %q", out)
	assert.Assert(t, strings.Contains(out, "every variant was killed"), "got %q", out)
}

func TestReportVariantSummaryCountsUnassessedSeparately(t *testing.T) {
	status, dump := captureStatus()

	reportVariantSummary([]variants.Result{
		{ID: "MUT-001", Killed: true, ExitCode: 1},
		{ID: "MUT-002"}, // survivor
		{ID: "MUT-003", Error: "sync: no route to host"},
	}, status)

	out := dump()
	// The unassessed variant counts as neither a kill nor a survivor.
	assert.Assert(t, strings.Contains(out, "1/3 variants killed"), "got %q", out)
	assert.Assert(t, strings.Contains(out, "not assessed"), "got %q", out)
	assert.Assert(t, !strings.Contains(out, "every variant was killed"), "got %q", out)
}
