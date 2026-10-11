package factory

import (
	"context"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestChooseAuthor(t *testing.T) {
	configured := config.CommitAuthor{Name: "Ada", Email: "ada@example.com"}
	developer := config.CommitAuthor{Name: "Dev", Email: "dev@example.com"}
	chunk := ResolvedAuthor{CommitAuthor: config.CommitAuthor{Name: defaultAuthorName, Email: defaultAuthorEmail}, Source: AuthorDefault}
	tests := []struct {
		name       string
		configured config.CommitAuthor
		developer  config.CommitAuthor
		want       ResolvedAuthor
	}{
		{"configured wins", configured, developer, ResolvedAuthor{configured, AuthorFromConfig}},
		{"developer when nothing configured", config.CommitAuthor{}, developer, ResolvedAuthor{developer, AuthorFromGit}},
		{"chunk when nobody is named", config.CommitAuthor{}, config.CommitAuthor{}, chunk},
		{"an identity without an email is skipped", config.CommitAuthor{}, config.CommitAuthor{Name: "Dev"}, chunk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, chooseAuthor(tt.configured, tt.developer), tt.want)
		})
	}
}

func TestResolveAuthor(t *testing.T) {
	// Keep the machine's own git identity out of the test.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	ctx := context.Background()
	root := newProject(t)

	got := ResolveAuthor(ctx, root, config.CommitAuthor{})
	assert.Equal(t, got.Source, AuthorDefault)

	gitOutput(t, root, "config", "user.name", "Dev Eloper")
	gitOutput(t, root, "config", "user.email", "dev@example.com")
	assert.Equal(t, ResolveAuthor(ctx, root, config.CommitAuthor{}),
		ResolvedAuthor{config.CommitAuthor{Name: "Dev Eloper", Email: "dev@example.com"}, AuthorFromGit})

	configured := config.CommitAuthor{Name: "Ada", Email: "ada@example.com"}
	assert.Equal(t, ResolveAuthor(ctx, root, configured), ResolvedAuthor{configured, AuthorFromConfig})
}

func TestWorktreeCommitsAsItsAuthor(t *testing.T) {
	ctx := context.Background()
	root := newProject(t)
	author := config.CommitAuthor{Name: "Dev Eloper", Email: "dev@example.com"}

	wt, err := CreateWorktree(ctx, WorktreeOptions{Root: root, Path: filepath.Join(t.TempDir(), "wt"), RunID: "run-1", Prompt: "add a flag", Author: author})
	assert.NilError(t, err)
	// The baseline holds the developer's uncommitted work, so it is a commit
	// chunk made, and it is published with the branch.
	assert.Equal(t, gitOutput(t, wt.Path, "log", "-1", "--format=%an <%ae> / %cn <%ce>", wt.Baseline),
		"Dev Eloper <dev@example.com> / Dev Eloper <dev@example.com>")

	writeFile(t, wt.Path, "flag.go", "package main\n")
	_, err = wt.Commit(ctx, "add a flag")
	assert.NilError(t, err)
	assert.Equal(t, gitOutput(t, wt.Path, "log", "-1", "--format=%an <%ae> / %cn <%ce>"),
		"Dev Eloper <dev@example.com> / Dev Eloper <dev@example.com>")
}
