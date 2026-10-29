package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

const (
	configWithCmds    = `{"commands":[{"name":"test","run":"go test ./..."}]}`
	configWithoutCmds = `{}`
)

// planProjectRepo is a git repository in a temp dir, with an origin when
// origin is true, config as its .chunk/config.json ("" writes none), and its
// review prompts directory as reviews describes: "prompt" holds one .md
// prompt, "empty" is an empty directory, "unreadable" has mode 000, and ""
// is no directory.
func planProjectRepo(t *testing.T, origin bool, config, reviews string) string {
	t.Helper()
	repo := gitrepo.SetupGitRepo(t, "org", "repo")
	if !origin {
		cmd := exec.Command("git", "remote", "remove", "origin")
		cmd.Dir = repo
		cmd.Env = gitrepo.GitEnv(repo)
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, string(out))
	}
	if config != "" {
		err := os.MkdirAll(filepath.Join(repo, ".chunk"), 0o755)
		assert.NilError(t, err)
		err = os.WriteFile(filepath.Join(repo, ".chunk", "config.json"), []byte(config), 0o644)
		assert.NilError(t, err)
	}
	dir := filepath.Join(repo, review.DefaultDir)
	writePrompt := func() {
		err := os.MkdirAll(dir, 0o755)
		assert.NilError(t, err)
		err = os.WriteFile(filepath.Join(dir, "correctness.md"), []byte("Check the change is correct.\n"), 0o644)
		assert.NilError(t, err)
	}
	switch reviews {
	case "":
	case "prompt":
		writePrompt()
	case "empty":
		err := os.MkdirAll(dir, 0o755)
		assert.NilError(t, err)
	case "unreadable":
		writePrompt()
		err := os.Chmod(dir, 0o000)
		assert.NilError(t, err)
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	default:
		t.Fatalf("unknown reviews %q", reviews)
	}
	return repo
}

// A plan's build needs an origin to push to, and review prompts, validation
// commands or both to check it with.
func TestCheckPlanProject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		origin  bool
		config  string
		reviews string
		wantErr string // "" accepts
	}{
		{
			name:    "no origin",
			config:  configWithCmds,
			reviews: "prompt",
			wantErr: "no git remote named origin",
		},
		{
			name:    "no config",
			origin:  true,
			reviews: "prompt",
			wantErr: msgValidateNotConfigured,
		},
		{
			name:    "unreadable config",
			origin:  true,
			config:  "{not json",
			wantErr: msgValidateNotConfigured,
		},
		{
			name:    "nothing to check",
			origin:  true,
			config:  configWithoutCmds,
			wantErr: "Nothing for factory to check",
		},
		{
			name:    "empty reviews dir and no commands",
			origin:  true,
			config:  configWithoutCmds,
			reviews: "empty",
			wantErr: "Nothing for factory to check",
		},
		{
			name:    "unreadable reviews dir",
			origin:  true,
			config:  configWithCmds,
			reviews: "unreadable",
			wantErr: "Could not read review prompts from",
		},
		{
			name:   "validation only",
			origin: true,
			config: configWithCmds,
		},
		{
			name:    "reviews only",
			origin:  true,
			config:  configWithoutCmds,
			reviews: "prompt",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.reviews == "unreadable" && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("mode 000 does not stop root or Windows from reading a directory")
			}
			repo := planProjectRepo(t, tc.origin, tc.config, tc.reviews)
			err := checkPlanProject(t.Context(), repo)
			if tc.wantErr == "" {
				assert.Check(t, err)
				return
			}
			assert.Assert(t, cmp.ErrorContains(err, tc.wantErr))
			assert.Check(t, !strings.Contains(err.Error(), "factory build"))
		})
	}
}
