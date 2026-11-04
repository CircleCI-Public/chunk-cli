package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/skills"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

// The steps chunk init runs, each reporting what it did through status. They
// stand alone so other commands can run the ones they need.

// gitHookCommand is what the git pre-commit hook runs.
const gitHookCommand = "chunk validate"

// projectSkills are the skills installed into a project.
var projectSkills = []string{"chunk-sidecar", "chunk-sidecar-setup", "chunk-validate-config"}

// writeGitHook makes the repository's pre-commit hook run chunk validate.
// gitCommonDir is the path gitutil.CommonDirCtx returns.
func writeGitHook(gitCommonDir string, status iostream.StatusFunc) error {
	outcome, err := gitutil.EnsurePreCommit(gitCommonDir, gitHookCommand)
	if err != nil {
		return fileError(".git/hooks/pre-commit", err)
	}
	const about = "Not tracked in git. Runs `chunk validate` locally before every commit you make."
	switch outcome {
	case gitutil.HookCreated:
		status(iostream.LevelDone, "Wrote .git/hooks/pre-commit")
		status(iostream.LevelInfo, about)
	case gitutil.HookAppended:
		status(iostream.LevelDone, "Updated .git/hooks/pre-commit")
		status(iostream.LevelInfo, about)
	case gitutil.HookUpToDate:
		status(iostream.LevelDone, "Git pre-commit hook already up to date")
	case gitutil.HookSymlink:
		status(iostream.LevelWarn, ".git/hooks/pre-commit is a symlink; skipping modification to avoid breaking your hook manager. Add `chunk validate` to the hook manually.")
	}
	return nil
}

// fileError phrases an error from writing one of the project's files for the
// user. path is relative to the project directory; err says which step failed.
func fileError(path string, err error) error {
	ue := &userError{msg: "Could not update " + path + ".", err: err}
	if errors.As(err, new(*fs.PathError)) {
		ue.suggestion = suggestionCheckPerms
	}
	return ue
}

// writeTestSuites scaffolds .circleci/test-suites.yml when the toolchain has a
// template. A failure is reported rather than returned.
func writeTestSuites(dir string, status iostream.StatusFunc) {
	outcome, err := validate.WriteTestSuites(dir)
	if err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not write .circleci/test-suites.yml: %v", err))
		return
	}
	switch outcome {
	case validate.TestSuitesWritten:
		status(iostream.LevelDone, "Wrote .circleci/test-suites.yml")
	case validate.TestSuitesExist:
		// Flush left like the lines around it, but dimmed: it is not news.
		status(iostream.LevelStep, ui.Dim(".circleci/test-suites.yml already exists, leaving as-is"))
	case validate.TestSuitesNoTemplate:
	}
}

// installSkills installs chunk's sidecar skills into the project for each
// agent it finds. A skill that cannot be installed is reported, not returned.
func installSkills(dir string, status iostream.StatusFunc) {
	for _, r := range skills.InstallByName(skills.ScopeProject, dir, projectSkills...) {
		if r.Skipped {
			continue
		}
		for _, name := range r.Installed {
			status(iostream.LevelDone, fmt.Sprintf("Installed %s skill for %s", name, r.Agent))
		}
		for _, name := range r.Updated {
			status(iostream.LevelDone, fmt.Sprintf("Updated %s skill for %s", name, r.Agent))
		}
		for _, msg := range r.Errors {
			status(iostream.LevelWarn, fmt.Sprintf("Could not install skill for %s: %s", r.Agent, msg))
		}
	}
}

// sidecarHint points the user at setting up the sidecar dev loop. Skipped
// when .circleci/test-suites.yml already exists. It is advice rather than a
// step, so it is set apart from the steps above it.
func sidecarHint(dir string, streams iostream.Streams) {
	if _, err := os.Stat(filepath.Join(dir, ".circleci", "test-suites.yml")); err == nil {
		return
	}
	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("Next step: set up a sidecar"))
	streams.ErrPrintln(ui.Dim("  Ask your AI coding agent to run the chunk-sidecar skill to spin up"))
	streams.ErrPrintln(ui.Dim("  a microVM, sync your repo, and run tests remotely."))
}
