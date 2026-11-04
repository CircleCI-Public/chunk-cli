package onboarding_test

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/onboarding"
)

func fileStore(t *testing.T) (onboarding.StateStore, string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := gitrepo.SetupGitRepo(t, "org", "repo")
	s, err := onboarding.FileStore(root)
	assert.NilError(t, err)
	dir, err := config.RepoDataDir(root)
	assert.NilError(t, err)
	return s, filepath.Join(dir, "onboarding.json")
}

func TestFileStoreMissing(t *testing.T) {
	s, _ := fileStore(t)
	assert.Check(t, cmp.DeepEqual(s.Load(), onboarding.State{Suggestions: onboarding.Generic}))
}

func TestFileStoreCorrupt(t *testing.T) {
	s, path := fileStore(t)
	err := os.MkdirAll(filepath.Dir(path), 0o700)
	assert.NilError(t, err)
	err = os.WriteFile(path, []byte("{"), 0o600)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(s.Load(), onboarding.State{Suggestions: onboarding.Generic}))
}

func TestFileStoreRoundTrip(t *testing.T) {
	s, path := fileStore(t)
	want := onboarding.State{
		PlanID:     "plan-1",
		Suggestion: "Add dark mode",
		Finished:   true,
		Suggestions: []onboarding.Suggestion{
			{Title: "Add keyboard shortcuts"},
			{Title: "Export a table to CSV"},
			{Title: "Add dark mode", Used: true},
		},
	}
	err := s.Save(want)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(s.Load(), want))

	fi, err := os.Stat(path)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(fi.Mode().Perm(), os.FileMode(0o600)))
}
