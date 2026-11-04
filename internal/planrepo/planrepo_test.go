package planrepo

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

// isolate points chunk's data dir at a temp dir and keeps the developer's git
// config, with its URL rewrites, out of the repos under test.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// initRepo makes dir a git repo whose origin is url ("" for none).
func initRepo(t *testing.T, dir, url string) {
	t.Helper()
	cmds := [][]string{{"init", "-q"}}
	if url != "" {
		cmds = append(cmds, []string{"remote", "add", "origin", url})
	}
	for _, args := range cmds {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, "git %v: %s", args, out)
	}
}

// checkout is a new git repo whose origin is url ("" for none), canonical.
func checkout(t *testing.T, url string) string {
	t.Helper()
	dir := config.CanonicalProjectRoot(t.TempDir())
	initRepo(t, dir, url)
	return dir
}

func TestMatches(t *testing.T) {
	isolate(t)
	for _, tc := range []struct {
		name    string
		url     string
		repos   []string
		wantOK  bool
		wantOwn string
	}{
		{
			name:    "ssh",
			url:     "git@github.com:circleci/factory.git",
			repos:   []string{"circleci/factory"},
			wantOK:  true,
			wantOwn: "circleci/factory",
		},
		{
			name:    "https",
			url:     "https://github.com/circleci/factory",
			repos:   []string{"circleci/factory"},
			wantOK:  true,
			wantOwn: "circleci/factory",
		},
		{
			name:    "case-insensitive",
			url:     "git@github.com:CircleCI-Public/chunk-cli.git",
			repos:   []string{"circleci-public/CHUNK-CLI"},
			wantOK:  true,
			wantOwn: "CircleCI-Public/chunk-cli",
		},
		{
			name:    "one of several",
			url:     "git@github.com:circleci/factory.git",
			repos:   []string{"circleci/a", "circleci/factory"},
			wantOK:  true,
			wantOwn: "circleci/factory",
		},
		{
			name:    "another repo",
			url:     "git@github.com:circleci/factory.git",
			repos:   []string{"circleci/other"},
			wantOwn: "circleci/factory",
		},
		{
			name:  "gitlab",
			url:   "git@gitlab.com:circleci/factory.git",
			repos: []string{"circleci/factory"},
		},
		{
			name:  "no origin",
			repos: []string{"circleci/factory"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, own := Matches(t.Context(), checkout(t, tc.url), tc.repos)
			assert.Check(t, cmp.Equal(ok, tc.wantOK))
			assert.Check(t, cmp.Equal(own, tc.wantOwn))
		})
	}
}

func TestMatchesMissingDir(t *testing.T) {
	isolate(t)
	ok, own := Matches(t.Context(), filepath.Join(t.TempDir(), "gone"), []string{"circleci/factory"})
	assert.Check(t, !ok)
	assert.Check(t, cmp.Equal(own, ""))
}

func TestNoCheckoutError(t *testing.T) {
	assert.Check(t, cmp.Equal((&NoCheckoutError{Repos: []string{"circleci/factory"}}).Error(),
		"plan builds in circleci/factory, and chunk knows no checkout of it; run chunk watch once in a checkout of circleci/factory"))
	assert.Check(t, cmp.Equal((&NoCheckoutError{Repos: []string{"circleci/a", "circleci/b"}}).Error(),
		"plan builds in one of circleci/a, circleci/b, and chunk knows no checkout of them; run chunk watch once in a checkout of one"))
}

func TestRoot(t *testing.T) {
	isolate(t)
	factoryA := checkout(t, "git@github.com:circleci/factory.git")
	factoryB := checkout(t, "https://github.com/circleci/factory")
	chunk := checkout(t, "git@github.com:CircleCI-Public/chunk-cli.git")
	gitlab := checkout(t, "git@gitlab.com:circleci/factory.git")
	bare := checkout(t, "")
	// A plan worktree under chunk's data dir, as validate would register it.
	data, err := config.AppData()
	assert.NilError(t, err)
	planWT := filepath.Join(data, "repo-x", "plans", "p1")
	err = os.MkdirAll(planWT, 0o755)
	assert.NilError(t, err)
	initRepo(t, planWT, "git@github.com:circleci/factory.git")

	for _, tc := range []struct {
		name    string
		repos   []string
		prefer  string
		known   []string
		hold    string // a known root given p1's worktree first
		want    string
		wantErr string
	}{
		{
			name:   "prefer matches",
			repos:  []string{"circleci/factory"},
			prefer: factoryB,
			known:  []string{factoryA, factoryB},
			want:   factoryB,
		},
		{
			name:   "case-insensitive",
			repos:  []string{"CircleCI/Factory"},
			prefer: factoryA,
			want:   factoryA,
		},
		{
			name:   "another checkout",
			repos:  []string{"circleci/factory"},
			prefer: chunk,
			known:  []string{chunk, factoryA},
			want:   factoryA,
		},
		{
			name:   "most recent first",
			repos:  []string{"circleci/factory"},
			prefer: chunk,
			known:  []string{factoryB, factoryA},
			want:   factoryB,
		},
		{
			name:   "holding the worktree wins over recency",
			repos:  []string{"circleci/factory"},
			prefer: chunk,
			known:  []string{factoryB, factoryA},
			hold:   factoryA,
			want:   factoryA,
		},
		{
			name:  "outside a repo",
			repos: []string{"circleci/factory"},
			known: []string{factoryA},
			want:  factoryA,
		},
		{
			name:   "no repos keeps prefer",
			prefer: chunk,
			want:   chunk,
		},
		{
			name: "no repos outside a repo",
		},
		{
			name:    "skips plan worktrees",
			repos:   []string{"circleci/factory"},
			prefer:  chunk,
			known:   []string{planWT},
			wantErr: "plan builds in circleci/factory, and chunk knows no checkout of it",
		},
		{
			name:   "skips non-GitHub, missing and vanished origins",
			repos:  []string{"circleci/factory"},
			prefer: chunk,
			known:  []string{gitlab, bare, "/does/not/exist", factoryA},
			want:   factoryA,
		},
		{
			name:   "any of several repos",
			repos:  []string{"circleci/a", "circleci/factory"},
			prefer: chunk,
			known:  []string{factoryA},
			want:   factoryA,
		},
		{
			name:    "none known",
			repos:   []string{"circleci/a", "circleci/b"},
			prefer:  chunk,
			known:   []string{chunk},
			wantErr: "plan builds in one of circleci/a, circleci/b",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.hold != "" {
				wt, err := factory.PlanWorktreePath(tc.hold, "p1")
				assert.NilError(t, err)
				err = os.MkdirAll(wt, 0o755)
				assert.NilError(t, err)
				t.Cleanup(func() { _ = os.RemoveAll(wt) })
			}
			got, err := Root(t.Context(), "p1", tc.repos, tc.prefer, tc.known)
			if tc.wantErr != "" {
				assert.Check(t, cmp.ErrorContains(err, tc.wantErr))
				var nc *NoCheckoutError
				ok := errors.As(err, &nc)
				assert.Check(t, ok)
				return
			}
			assert.Check(t, err)
			assert.Check(t, cmp.Equal(got, tc.want))
		})
	}
}
