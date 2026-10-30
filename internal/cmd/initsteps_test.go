package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
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
	vcs, err := detectVCS(context.Background(), gitRepo(t, "https://github.com/acme/widget.git"), status)
	assert.NilError(t, err)
	assert.DeepEqual(t, vcs, &config.VCSConfig{Org: "acme", Repo: "widget"})
	assert.Assert(t, strings.Contains(out.String(), "Detected repository: "+ui.Bold("acme/widget")), out.String())
}

func TestDetectVCSWithoutOrigin(t *testing.T) {
	status, out := recordStatus()
	vcs, err := detectVCS(context.Background(), gitRepo(t, ""), status)
	assert.NilError(t, err)
	assert.Assert(t, vcs == nil)
	assert.Assert(t, strings.Contains(out.String(), "Could not detect VCS info"), out.String())
}

func TestWriteGitHookWarnsAboutSymlink(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("symlink test not meaningful as root")
	}
	dir := gitRepo(t, "")
	hooksDir := filepath.Join(dir, ".git", "hooks")
	assert.NilError(t, os.MkdirAll(hooksDir, 0o755))
	target := filepath.Join(dir, "pre-commit-real")
	assert.NilError(t, os.WriteFile(target, []byte("#!/bin/sh\nnpm test\n"), 0o755))
	assert.NilError(t, os.Symlink(target, filepath.Join(hooksDir, "pre-commit")))

	status, out := recordStatus()
	assert.NilError(t, writeGitHook(filepath.Join(dir, ".git"), status))
	assert.Assert(t, strings.Contains(out.String(), "symlink"), out.String())
}

// TestInstallSkillsUsesProjectScope verifies that installSkills writes skills
// into the project's .claude/skills/ directory, not into the user's home
// directory. It once called InstallByName(ScopeUser, homeDir, ...), which
// diverged from the ScopeProject default used by the skills subcommand.
func TestInstallSkillsUsesProjectScope(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	status, _ := recordStatus()
	installSkills(workDir, status)

	skillPath := filepath.Join(workDir, ".claude", "skills", "chunk-sidecar", "SKILL.md")
	_, err := os.Stat(skillPath)
	assert.NilError(t, err, "expected skill installed at %s", skillPath)

	homePath := filepath.Join(home, ".claude", "skills", "chunk-sidecar", "SKILL.md")
	_, err = os.Stat(homePath)
	assert.Assert(t, os.IsNotExist(err), "skill must not be installed under $HOME")
}

// TestInstallSkillsInstallsSidecarSetup verifies the onboarding wizard ships
// alongside chunk-sidecar. Without it, chunk-sidecar's first-time setup path
// routes to a skill that is not installed.
func TestInstallSkillsInstallsSidecarSetup(t *testing.T) {
	workDir := t.TempDir()
	t.Setenv(config.EnvHome, t.TempDir())

	status, _ := recordStatus()
	installSkills(workDir, status)

	for _, name := range []string{"chunk-sidecar", "chunk-sidecar-setup"} {
		skillPath := filepath.Join(workDir, ".claude", "skills", name, "SKILL.md")
		_, err := os.Stat(skillPath)
		assert.NilError(t, err, "expected skill installed at %s", skillPath)
	}
}

// Notes print flush left when there is no source line for them to sit under.
func TestReportDetectionSourceNotesWithoutSource(t *testing.T) {
	ui.SetColorEnabled(false)
	t.Cleanup(func() { ui.SetColorEnabled(true) })
	streams, _, errOut := testStreams()

	reportDetectionSource(validate.Detection{Notes: []string{"config is dynamic"}}, initStatus(streams))
	assert.Equal(t, errOut.String(), "config is dynamic\n")

	errOut.Reset()
	reportDetectionSource(validate.Detection{Source: "Makefile", Notes: []string{"no lint target"}}, initStatus(streams))
	assert.Equal(t, errOut.String(), "Detected commands from Makefile\n  no lint target\n")
}
