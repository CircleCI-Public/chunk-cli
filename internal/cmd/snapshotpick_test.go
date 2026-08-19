package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/envspec"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func TestSnapshotCriteriaFromGitRemote(t *testing.T) {
	dir := gitrepo.SetupGitRepo(t, "CircleCI-Public", "chunk-cli")

	got := snapshotCriteria(dir, nil)
	assert.Equal(t, got.Repo, "chunk-cli")
}

func TestSnapshotCriteriaFallsBackToDirectoryName(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "my-project")
	assert.NilError(t, os.Mkdir(dir, 0o755))

	got := snapshotCriteria(dir, nil)
	assert.Equal(t, got.Repo, "my-project")
}

func TestSnapshotCriteriaFallsBackToConfiguredRepo(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "checkout")
	assert.NilError(t, os.Mkdir(dir, 0o755))

	cfg := &config.ProjectConfig{VCS: &config.VCSConfig{Repo: "chunk-cli"}}
	got := snapshotCriteria(dir, cfg)
	assert.Equal(t, got.Repo, "chunk-cli")
}

// The saved environment is authoritative: re-walking the tree can disagree with
// what setup actually provisioned, and the snapshot was built from the latter.
func TestSnapshotCriteriaPrefersSavedStack(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644))

	cfg := &config.ProjectConfig{Environment: &envspec.Environment{Stack: "python"}}
	got := snapshotCriteria(dir, cfg)
	assert.Equal(t, got.Stack, "python")
}

func TestSnapshotCriteriaDetectsStackWhenUnsaved(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n"), 0o644))

	got := snapshotCriteria(dir, nil)
	assert.Equal(t, got.Stack, "go")
}

// An undetectable stack must not become the literal string "unknown", which
// would then be matched against snapshot names.
func TestSnapshotCriteriaBlanksUnknownStack(t *testing.T) {
	dir := t.TempDir()

	got := snapshotCriteria(dir, nil)
	assert.Equal(t, got.Stack, "")
}

// Without a client or an org there is nothing to list, and the caller must be
// left on the default image rather than erroring out.
func TestAutoSelectSnapshotImageNoOpWithoutOrg(t *testing.T) {
	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	noopStatus := func(iostream.Level, string) {}

	assert.Equal(t, autoSelectSnapshotImage(context.Background(), nil, "org-abc", t.TempDir(), noopStatus, streams), "")
	assert.Equal(t, autoSelectSnapshotImage(context.Background(), nil, "", t.TempDir(), noopStatus, streams), "")
}

func TestSnapshotMissHint(t *testing.T) {
	tests := []struct {
		criteria sidecar.SnapshotCriteria
		want     string
	}{
		{sidecar.SnapshotCriteria{Repo: "chunk-cli", Stack: "go"}, "no snapshot matches chunk-cli or go; using the default image"},
		{sidecar.SnapshotCriteria{Repo: "chunk-cli"}, "no snapshot matches chunk-cli; using the default image"},
		{sidecar.SnapshotCriteria{Stack: "go"}, "no snapshot matches go; using the default image"},
		{sidecar.SnapshotCriteria{}, "no snapshot to match against; using the default image"},
	}
	for _, tt := range tests {
		assert.Equal(t, snapshotMissHint(tt.criteria), tt.want)
	}
}
