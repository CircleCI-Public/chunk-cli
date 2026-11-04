package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// recordStatus returns a StatusFunc that records each message, one per line.
func recordStatus() (iostream.StatusFunc, *bytes.Buffer) {
	var buf bytes.Buffer
	return func(_ iostream.Level, msg string) { fmt.Fprintln(&buf, msg) }, &buf
}

// gitRepo is a fresh git repository, with origin as its origin when given.
func gitRepo(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{{"init"}}
	if origin != "" {
		cmds = append(cmds, []string{"remote", "add", "origin", origin})
	}
	for _, args := range cmds {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestDetectVCS(t *testing.T) {
	status, out := recordStatus()
	vcs, err := DetectVCS(context.Background(), gitRepo(t, "https://github.com/acme/widget.git"), status)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(vcs, &config.VCSConfig{Org: "acme", Repo: "widget"}))
	assert.Check(t, cmp.Contains(out.String(), "Detected repository: "+ui.Bold("acme/widget")))
}

func TestDetectVCSWithoutOrigin(t *testing.T) {
	status, out := recordStatus()
	vcs, err := DetectVCS(context.Background(), gitRepo(t, ""), status)
	assert.NilError(t, err)
	assert.Check(t, cmp.Nil(vcs))
	assert.Check(t, cmp.Contains(out.String(), "Could not detect VCS info"))
}
