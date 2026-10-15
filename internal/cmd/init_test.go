package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func testStreams() (iostream.Streams, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return iostream.Streams{Out: &out, Err: &errOut}, &out, &errOut
}

func setupShellEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvShell, "/bin/zsh")
	return home
}

func TestCompletionInstalledFalseWhenNoRCFile(t *testing.T) {
	setupShellEnv(t)

	installed, err := completionInstalled()
	assert.NilError(t, err)
	assert.Assert(t, !installed)
}

func TestCompletionInstalledTrueWhenTagPresent(t *testing.T) {
	home := setupShellEnv(t)
	rcFile := filepath.Join(home, ".zshrc")
	assert.NilError(t, os.WriteFile(rcFile, []byte(completionTag+"\nsource <(chunk completion zsh)\n"), 0o644))

	installed, err := completionInstalled()
	assert.NilError(t, err)
	assert.Assert(t, installed)
}

func TestCompletionInstalledUnsupportedShell(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvShell, "/bin/fish")

	_, err := completionInstalled()
	assert.Assert(t, err != nil)
}

func TestInstallCompletionWritesRCFile(t *testing.T) {
	home := setupShellEnv(t)
	rcFile := filepath.Join(home, ".zshrc")
	streams, _, errOut := testStreams()

	err := installCompletion(streams)
	assert.NilError(t, err)

	data, err := os.ReadFile(rcFile)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), completionTag))
	assert.Assert(t, strings.Contains(string(data), "source <(chunk completion zsh)"))
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte(ui.Success("Completion installed."))))
}

func TestInstallCompletionSkipsWhenAlreadyInstalled(t *testing.T) {
	home := setupShellEnv(t)
	rcFile := filepath.Join(home, ".zshrc")
	existing := completionTag + "\nsource <(chunk completion zsh)\n"
	assert.NilError(t, os.WriteFile(rcFile, []byte(existing), 0o644))

	streams, _, errOut := testStreams()
	err := installCompletion(streams)
	assert.NilError(t, err)

	// File unchanged.
	data, err := os.ReadFile(rcFile)
	assert.NilError(t, err)
	assert.Equal(t, string(data), existing)
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte("already installed")))
}

func TestPrintInitSummaryWithCommands(t *testing.T) {
	ui.SetColorEnabled(false)
	defer ui.SetColorEnabled(true)

	commands := []config.Command{
		{Name: "test", Run: "task test"},
		{Name: "lint", Run: "task lint"},
	}
	streams, _, errOut := testStreams()
	printInitSummary(commands, streams)

	out := errOut.String()
	assert.Assert(t, strings.Contains(out, "Validation commands:"), "missing section header")
	assert.Assert(t, strings.Contains(out, "test"), "missing test command name")
	assert.Assert(t, strings.Contains(out, "task test"), "missing test command run")
	assert.Assert(t, strings.Contains(out, "lint"), "missing lint command name")
	assert.Assert(t, strings.Contains(out, "task lint"), "missing lint command run")
	assert.Assert(t, strings.Contains(out, ".chunk/config.json"), "missing config path")
	assert.Assert(t, strings.Contains(out, "chunk validate"), "missing validate hint")
	assert.Assert(t, strings.Contains(out, "chunk validate --remote"), "missing remote hint")
	assert.Assert(t, !strings.Contains(out, "CircleCI"), "should not mention CircleCI in init output")
	assert.Assert(t, strings.Contains(out, "chunk validate --remote test"), "missing per-command remote hint")
	assert.Assert(t, strings.Contains(out, "chunk validate --remote lint"), "missing per-command remote hint")
	assert.Assert(t, !strings.Contains(out, "<name>"), "should not show placeholder when commands are known")
}

func TestPrintInitSummaryNoCommands(t *testing.T) {
	ui.SetColorEnabled(false)
	defer ui.SetColorEnabled(true)

	streams, _, errOut := testStreams()
	printInitSummary(nil, streams)

	out := errOut.String()
	assert.Assert(t, !strings.Contains(out, "Validation commands:"), "should not show command table when none configured")
	assert.Assert(t, strings.Contains(out, ".chunk/config.json"), "missing config path")
	assert.Assert(t, strings.Contains(out, "chunk validate"), "missing validate hint")
}

func newDetectOrgIDFake(t *testing.T) (*fakes.FakeCircleCI, config.ResolvedConfig) {
	t.Helper()
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	rc := config.ResolvedConfig{CircleCIToken: "test-token", CircleCIBaseURL: srv.URL}
	return cci, rc
}

func TestDetectOrgID_NoAuth(t *testing.T) {
	streams, _, errOut := testStreams()
	orgID := detectOrgID(context.Background(), config.ResolvedConfig{}, streams)
	assert.Equal(t, orgID, "")
	assert.Equal(t, errOut.Len(), 0)
}

func TestDetectOrgID_SingleOrg(t *testing.T) {
	cci, rc := newDetectOrgIDFake(t)
	cci.Collaborations = []fakes.Collaboration{{ID: "org-xyz", Name: "myorg"}}

	streams, _, errOut := testStreams()
	orgID := detectOrgID(context.Background(), rc, streams)
	assert.Equal(t, orgID, "org-xyz")
	assert.Equal(t, errOut.Len(), 0)
}

func TestDetectOrgID_MultipleOrgs_NoTTY(t *testing.T) {
	cci, rc := newDetectOrgIDFake(t)
	cci.Collaborations = []fakes.Collaboration{
		{ID: "org-1", Name: "first"},
		{ID: "org-2", Name: "second"},
	}

	streams, _, errOut := testStreams()
	orgID := detectOrgID(context.Background(), rc, streams)
	assert.Equal(t, orgID, "")
	assert.Equal(t, errOut.Len(), 0, "ErrNoTTY should be suppressed silently")
}

func TestDetectOrgID_APIError(t *testing.T) {
	cci, rc := newDetectOrgIDFake(t)
	cci.CollaborationsStatusCode = 500

	streams, _, errOut := testStreams()
	orgID := detectOrgID(context.Background(), rc, streams)
	assert.Equal(t, orgID, "")
	assert.Assert(t, strings.Contains(errOut.String(), "Could not detect org ID"))
}

func TestInstallCompletionBashWritesRCFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvShell, "/bin/bash")

	// Create .bashrc so detectShell prefers it over .bash_profile.
	rcFile := filepath.Join(home, ".bashrc")
	assert.NilError(t, os.WriteFile(rcFile, []byte("# existing config\n"), 0o644))

	streams, _, errOut := testStreams()
	err := installCompletion(streams)
	assert.NilError(t, err)

	data, err := os.ReadFile(rcFile)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), completionTag))
	assert.Assert(t, strings.Contains(string(data), "source <(chunk completion bash)"))
	assert.Assert(t, bytes.Contains(errOut.Bytes(), []byte(ui.Success("Completion installed."))))
}

func TestDetectOrgID_NoOrgs_NoTTYSuggestsOrgCreate(t *testing.T) {
	stubPromptOrgName(t, func(iostream.Streams) (string, error) { return "", ui.ErrNoTTY })
	_, rc := newDetectOrgIDFake(t)

	streams, _, errOut := testStreams()
	orgID := detectOrgID(context.Background(), rc, streams)
	assert.Equal(t, orgID, "")
	assert.Assert(t, strings.Contains(errOut.String(), "Could not detect org ID: No organizations found."), errOut.String())
	assert.Assert(t, strings.Contains(errOut.String(), "chunk org create"), errOut.String())
}

func TestConfirmDiffShowsChangeAndAsks(t *testing.T) {
	ui.SetColorEnabled(false)
	defer ui.SetColorEnabled(true)

	streams, _, errOut := testStreams()
	var asked string
	confirm := confirmDiff(streams, func(label string, defaultYes bool) (bool, error) {
		asked = label
		assert.Assert(t, !defaultYes, "a change to existing settings must default to no")
		return true, nil
	})

	ok, err := confirm(".claude/settings.json", "--- a\n+++ b\n+added")
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, asked, "Apply changes to .claude/settings.json?")
	assert.Assert(t, strings.Contains(errOut.String(), "Changes to .claude/settings.json:"), errOut.String())
	assert.Assert(t, strings.Contains(errOut.String(), "+added"), errOut.String())
}

func TestGitDirError(t *testing.T) {
	ue, ok := errors.AsType[*userError](gitDirError(fmt.Errorf("%w: exit status 128", gitutil.ErrNotGitRepo)))
	assert.Assert(t, ok)
	assert.Equal(t, ue.msg, "Not a git repository.")
	assert.Equal(t, ue.suggestion, suggestionGitRepo)

	ue, ok = errors.AsType[*userError](gitDirError(errors.New("git rev-parse --git-common-dir: exit status 1")))
	assert.Assert(t, ok)
	assert.Equal(t, ue.msg, "Could not determine git directory.")
}

func TestFileError(t *testing.T) {
	denied := fmt.Errorf("write .claude/settings.json: %w", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
	ue, ok := errors.AsType[*userError](fileError(".claude/settings.json", denied))
	assert.Assert(t, ok)
	assert.Equal(t, ue.msg, "Could not update .claude/settings.json.")
	assert.Equal(t, ue.suggestion, suggestionCheckPerms)

	// A file that cannot be merged is not a permissions problem.
	ue, ok = errors.AsType[*userError](fileError(".codex/hooks.json", errors.New("merge .codex/hooks.json: invalid character")))
	assert.Assert(t, ok)
	assert.Equal(t, ue.msg, "Could not update .codex/hooks.json.")
	assert.Equal(t, ue.suggestion, "")
	assert.Assert(t, strings.Contains(ue.Error(), "invalid character"), ue.Error())
}
