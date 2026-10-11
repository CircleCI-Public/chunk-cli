package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/smeltdev/smelt/internal/anthropic"
	"github.com/smeltdev/smelt/internal/authprompt"
	"github.com/smeltdev/smelt/internal/brand"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/gitexec"
	"github.com/smeltdev/smelt/internal/gitremote"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/settings"
	"github.com/smeltdev/smelt/internal/skills"
	"github.com/smeltdev/smelt/internal/ui"
	"github.com/smeltdev/smelt/internal/validate"
)

// confirmFunc asks the user a yes/no question. Matches ui.Confirm signature.
type confirmFunc func(label string, defaultYes bool) (bool, error)

// withTrailingNewline returns a copy of data with a trailing newline appended.
// Uses a copy to avoid mutating the original slice's backing array.
func withTrailingNewline(data []byte) []byte {
	buf := make([]byte, len(data)+1)
	copy(buf, data)
	buf[len(data)] = '\n'
	return buf
}

// writeSettings writes .claude/settings.json for the project.
// When settings.json already exists, it computes a merge, shows the user
// a before/after comparison, and prompts for confirmation. On decline or
// non-TTY, falls back to writing settings.example.json.
func writeSettings(workDir string, commands []config.Command, streams iostream.Streams, confirm confirmFunc) error {
	generated, err := settings.Build(commands)
	if err != nil {
		return &userError{msg: "Could not build .claude/settings.json.", err: fmt.Errorf("build settings: %w", err)}
	}

	dir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &userError{
			msg:        "Could not create .claude directory.",
			suggestion: suggestionCheckPerms,
			err:        fmt.Errorf("create .claude dir: %w", err),
		}
	}

	path := filepath.Join(dir, "settings.json")
	existing, readErr := os.ReadFile(path)
	if readErr != nil {
		if !errors.Is(readErr, fs.ErrNotExist) {
			return &userError{
				msg:        "Could not read .claude/settings.json.",
				suggestion: suggestionCheckPerms,
				err:        fmt.Errorf("read existing settings.json: %w", readErr),
			}
		}
		// No existing file — write directly.
		if err := os.WriteFile(path, withTrailingNewline(generated), 0o644); err != nil {
			return &userError{
				msg:        "Could not write .claude/settings.json.",
				suggestion: suggestionCheckPerms,
				err:        fmt.Errorf("write settings.json: %w", err),
			}
		}
		streams.ErrPrintln(ui.Success("Wrote .claude/settings.json"))
		streams.ErrPrintln(ui.Dim("  Hidden Claude Code config. Runs `smelt validate` before commits and after each agent session."))
		return nil
	}

	// Existing file found — compute merge.
	result, err := settings.Merge(existing, generated)
	if err != nil {
		return &userError{msg: "Could not merge .claude/settings.json.", err: fmt.Errorf("merge settings: %w", err)}
	}

	if !result.Changed {
		streams.ErrPrintln(ui.Success("Settings already up to date"))
		return nil
	}

	// Show colored unified diff of changes.
	diff := settings.Diff(result.Original, result.Merged)
	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("Changes to .claude/settings.json:"))
	streams.ErrPrintln("")
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			streams.ErrPrintln(ui.Bold(line))
		case strings.HasPrefix(line, "@@"):
			streams.ErrPrintln(ui.Cyan(line))
		case strings.HasPrefix(line, "+"):
			streams.ErrPrintln(ui.Green(line))
		case strings.HasPrefix(line, "-"):
			streams.ErrPrintln(ui.Red(line))
		default:
			streams.ErrPrintln(line)
		}
	}

	// Prompt for confirmation.
	apply, confirmErr := confirm("Apply changes to .claude/settings.json?", false)
	if confirmErr != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not confirm: %v", confirmErr)))
		return writeSettingsExample(dir, generated, streams)
	}
	if !apply {
		return nil
	}

	if err := os.WriteFile(path, withTrailingNewline(result.Merged), 0o644); err != nil {
		return &userError{
			msg:        "Could not write .claude/settings.json.",
			suggestion: suggestionCheckPerms,
			err:        fmt.Errorf("write settings.json: %w", err),
		}
	}
	streams.ErrPrintln(ui.Success("Updated .claude/settings.json"))
	streams.ErrPrintln(ui.Dim("  Hidden Claude Code config. Runs `smelt validate` before commits and after each agent session."))
	return nil
}

// codexInstalled reports whether Codex appears to be installed on this machine.
// It checks for the binary on PATH and for the global ~/.codex settings directory.
func codexInstalled(homeDir string) bool {
	if _, err := exec.LookPath("codex"); err == nil {
		return true
	}
	if homeDir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".codex")); err == nil {
		return true
	}
	return false
}

// writeCodexHooks writes .codex/hooks.json for the project.
// Uses the same merge/confirm/fallback pattern as writeSettings.
func writeCodexHooks(workDir string, commands []config.Command, streams iostream.Streams, confirm confirmFunc) error {
	generated, err := settings.BuildCodex(commands)
	if err != nil {
		return &userError{msg: "Could not build .codex/hooks.json.", err: fmt.Errorf("build codex hooks: %w", err)}
	}

	dir := filepath.Join(workDir, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &userError{
			msg:        "Could not create .codex directory.",
			suggestion: suggestionCheckPerms,
			err:        fmt.Errorf("create .codex dir: %w", err),
		}
	}

	path := filepath.Join(dir, "hooks.json")
	existing, readErr := os.ReadFile(path)
	if readErr != nil {
		if !errors.Is(readErr, fs.ErrNotExist) {
			return &userError{
				msg:        "Could not read .codex/hooks.json.",
				suggestion: suggestionCheckPerms,
				err:        fmt.Errorf("read existing hooks.json: %w", readErr),
			}
		}
		if err := os.WriteFile(path, withTrailingNewline(generated), 0o644); err != nil {
			return &userError{
				msg:        "Could not write .codex/hooks.json.",
				suggestion: suggestionCheckPerms,
				err:        fmt.Errorf("write hooks.json: %w", err),
			}
		}
		streams.ErrPrintln(ui.Success("Wrote .codex/hooks.json"))
		streams.ErrPrintln(ui.Dim("  Hidden Codex config. Same hooks as .claude/settings.json, for Codex sessions."))
		return nil
	}

	result, err := settings.MergeCodex(existing, generated)
	if err != nil {
		return &userError{msg: "Could not merge .codex/hooks.json.", err: fmt.Errorf("merge codex hooks: %w", err)}
	}

	if !result.Changed {
		streams.ErrPrintln(ui.Success("Codex hooks already up to date"))
		return nil
	}

	diff := settings.Diff(result.Original, result.Merged)
	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("Changes to .codex/hooks.json:"))
	streams.ErrPrintln("")
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			streams.ErrPrintln(ui.Bold(line))
		case strings.HasPrefix(line, "@@"):
			streams.ErrPrintln(ui.Cyan(line))
		case strings.HasPrefix(line, "+"):
			streams.ErrPrintln(ui.Green(line))
		case strings.HasPrefix(line, "-"):
			streams.ErrPrintln(ui.Red(line))
		default:
			streams.ErrPrintln(line)
		}
	}

	apply, confirmErr := confirm("Apply changes to .codex/hooks.json?", false)
	if confirmErr != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not confirm: %v", confirmErr)))
		return writeCodexHooksExample(dir, generated, streams)
	}
	if !apply {
		return nil
	}

	if err := os.WriteFile(path, withTrailingNewline(result.Merged), 0o644); err != nil {
		return &userError{
			msg:        "Could not write .codex/hooks.json.",
			suggestion: suggestionCheckPerms,
			err:        fmt.Errorf("write hooks.json: %w", err),
		}
	}
	streams.ErrPrintln(ui.Success("Updated .codex/hooks.json"))
	streams.ErrPrintln(ui.Dim("  Hidden Codex config. Same hooks as .claude/settings.json, for Codex sessions."))
	return nil
}

// writeCodexHooksExample writes hooks.example.json as a fallback when the user
// declines to apply changes or when there is no TTY.
func writeCodexHooksExample(dir string, data []byte, streams iostream.Streams) error {
	exPath := filepath.Join(dir, "hooks.example.json")
	if err := os.WriteFile(exPath, withTrailingNewline(data), 0o644); err != nil {
		return &userError{
			msg: "Could not write .codex/hooks.example.json.",
			err: fmt.Errorf("write hooks.example.json: %w", err),
		}
	}
	streams.ErrPrintln(ui.Success("Wrote .codex/hooks.example.json (existing hooks.json preserved)"))
	return nil
}

// writeSettingsExample writes settings.example.json as a fallback.
func writeSettingsExample(dir string, data []byte, streams iostream.Streams) error {
	exPath := filepath.Join(dir, "settings.example.json")
	if err := os.WriteFile(exPath, withTrailingNewline(data), 0o644); err != nil {
		return &userError{
			msg: "Could not write .claude/settings.example.json.",
			err: fmt.Errorf("write settings.example.json: %w", err),
		}
	}
	streams.ErrPrintln(ui.Success("Wrote .claude/settings.example.json (existing settings.json preserved)"))
	return nil
}

// poolGitignoreEntries are machine-local pool state files under .smelt/.
// config.json and other project config must stay tracked.
var poolGitignoreEntries = []string{
	".smelt/*-pool.json",
}

// ensureGitignoreEntries appends validate pool patterns to .gitignore if they
// are not already present. Does not ignore .smelt/config.json.
func ensureGitignoreEntries(workDir string, streams iostream.Streams) error {
	path := filepath.Join(workDir, ".gitignore")

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read .gitignore: %w", err)
	}

	content := string(existing)
	var toAdd []string
	for _, entry := range poolGitignoreEntries {
		if !strings.Contains(content, entry) {
			toAdd = append(toAdd, entry)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open .gitignore: %w", err)
	}
	defer func() { _ = f.Close() }()

	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}

	if _, err := f.WriteString("\n# smelt local pool state\n"); err != nil {
		return err
	}
	for _, entry := range toAdd {
		if _, err := f.WriteString(entry + "\n"); err != nil {
			return err
		}
	}

	streams.ErrPrintln(ui.Success("Updated .gitignore with local pool state patterns"))
	streams.ErrPrintln(ui.Dim("  Machine-local sidecar pool IDs; not for commit. Keeps .smelt/config.json tracked."))
	return nil
}

// updateInitGitignore keeps a best-effort housekeeping failure from stopping
// initialization while still making it visible to the user.
func updateInitGitignore(workDir string, streams iostream.Streams) {
	if err := ensureGitignoreEntries(workDir, streams); err != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not update .gitignore: %v", err)))
	}
}

// initPlan is which backend-dependent steps `smelt init` runs. Everything that
// reaches CircleCI, or sets a project up for it, is gated on the circleci
// backend the user chose, so the plan is decided from that choice alone.
type initPlan struct {
	DetectOrgID bool
	TestSuites  bool // scaffold or hint at .circleci/test-suites.yml
	RemoteHints bool // mention `smelt validate --remote` in the summary
	Skills      []string
}

// Skills init installs. The sidecar ones describe CircleCI cloud sidecars, so
// they only go in for a project that chose that backend.
var (
	genericInitSkills  = []string{"smelt-validate-config"}
	circleCIInitSkills = []string{"smelt-sidecar", "smelt-sidecar-setup"}
)

// planInit decides the backend-dependent steps. The backend is chosen before
// any of them runs.
func planInit(circleCI bool) initPlan {
	if !circleCI {
		return initPlan{Skills: genericInitSkills}
	}
	return initPlan{
		DetectOrgID: true,
		TestSuites:  true,
		RemoteHints: true,
		Skills:      append(append([]string(nil), circleCIInitSkills...), genericInitSkills...),
	}
}

func installSkillsStep(workDir string, streams iostream.Streams, names []string) {
	if len(names) == 0 {
		streams.ErrPrintln(ui.Dim("No agent skills apply to this backend; skipping skill installation"))
		return
	}
	for _, r := range skills.InstallByName(skills.ScopeProject, workDir, names...) {
		if r.Skipped {
			continue
		}
		for _, name := range r.Installed {
			streams.ErrPrintln(ui.Success(fmt.Sprintf("Installed %s skill for %s", name, r.Agent)))
		}
		for _, name := range r.Updated {
			streams.ErrPrintln(ui.Success(fmt.Sprintf("Updated %s skill for %s", name, r.Agent)))
		}
		for _, msg := range r.Errors {
			streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not install skill for %s: %s", r.Agent, msg)))
		}
	}
}

// writeTestSuites scaffolds .circleci/test-suites.yml for CircleCI Smarter
// Testing whenever the toolchain has a known template, creating .circleci/
// if it does not already exist. It never overwrites an existing
// test-suites.yml.
func writeTestSuites(workDir string, streams iostream.Streams) error {
	template := validate.TestSuitesTemplate(workDir)
	if template == "" {
		return nil
	}

	circleDir := filepath.Join(workDir, ".circleci")
	path := filepath.Join(circleDir, "test-suites.yml")
	if _, err := os.Stat(path); err == nil {
		streams.ErrPrintln(ui.Dim(".circleci/test-suites.yml already exists, leaving as-is"))
		return nil
	}

	if err := os.MkdirAll(circleDir, 0o755); err != nil {
		return fmt.Errorf("create .circleci dir: %w", err)
	}

	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return fmt.Errorf("write test-suites.yml: %w", err)
	}
	streams.ErrPrintln(ui.Success("Wrote .circleci/test-suites.yml"))
	return nil
}

// printTestSuitesHint prints onboarding guidance for setting up the sidecar
// dev loop. Skipped when .circleci/test-suites.yml already exists.
func printTestSuitesHint(workDir string, streams iostream.Streams) {
	if _, err := os.Stat(filepath.Join(workDir, ".circleci", "test-suites.yml")); err == nil {
		return
	}
	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("Next step: set up a sidecar"))
	streams.ErrPrintln(ui.Dim("  Ask your AI coding agent to run the smelt-sidecar skill to spin up"))
	streams.ErrPrintln(ui.Dim("  a microVM, sync your repo, and run tests remotely."))
}

// writeAllHookFiles writes hook config files for all supported agents.
// Cursor reads .claude/settings.json natively so no extra file is needed for it.
// Codex hooks are only written when Codex is installed or the project already
// has a .codex directory.
func writeAllHookFiles(workDir string, commands []config.Command, streams iostream.Streams) error {
	if err := writeSettings(workDir, commands, streams, ui.Confirm); err != nil {
		return err
	}
	homeDir := os.Getenv(config.EnvHome)
	_, codexDirErr := os.Stat(filepath.Join(workDir, ".codex"))
	if codexInstalled(homeDir) || codexDirErr == nil {
		if err := writeCodexHooks(workDir, commands, streams, ui.Confirm); err != nil {
			return err
		}
	}
	return nil
}

const gitHookContent = "#!/bin/sh\n" + brand.Command + " validate\n"

// writeGitHook writes a pre-commit hook to the repository's git hooks
// directory. gitCommonDir must be the resolved path returned by
// `git rev-parse --git-common-dir`, so the hook lands in the shared hooks
// directory even when running from a worktree. If a pre-commit hook already
// exists and calls smelt validate it is left as-is; if it exists but does not
// call smelt validate, the call is appended preserving the existing permissions.
func writeGitHook(gitCommonDir string, streams iostream.Streams) error {
	hooksDir := filepath.Join(gitCommonDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return &userError{
			msg:        "Could not create git hooks directory.",
			suggestion: suggestionCheckPerms,
			err:        fmt.Errorf("create hooks dir: %w", err),
		}
	}

	path := filepath.Join(hooksDir, "pre-commit")
	existing, readErr := os.ReadFile(path)
	if readErr != nil {
		if !errors.Is(readErr, fs.ErrNotExist) {
			return &userError{
				msg:        "Could not read .git/hooks/pre-commit.",
				suggestion: suggestionCheckPerms,
				err:        fmt.Errorf("read pre-commit hook: %w", readErr),
			}
		}
		if err := os.WriteFile(path, []byte(gitHookContent), 0o755); err != nil {
			return &userError{
				msg:        "Could not write .git/hooks/pre-commit.",
				suggestion: suggestionCheckPerms,
				err:        fmt.Errorf("write pre-commit hook: %w", err),
			}
		}
		streams.ErrPrintln(ui.Success("Wrote .git/hooks/pre-commit"))
		streams.ErrPrintln(ui.Dim("  Not tracked in git. Runs `smelt validate` locally before every commit you make."))
		return nil
	}

	if strings.Contains(string(existing), brand.Command+" validate") {
		streams.ErrPrintln(ui.Success("Git pre-commit hook already up to date"))
		return nil
	}

	// Guard: do not modify a symlink. It is managed by another tool (e.g.
	// husky, lefthook). Overwriting the path would replace the symlink with a
	// regular file, silently breaking the hook manager's setup.
	linfo, lstatErr := os.Lstat(path)
	if lstatErr != nil && !errors.Is(lstatErr, fs.ErrNotExist) {
		return &userError{msg: "Could not stat .git/hooks/pre-commit.", suggestion: suggestionCheckPerms, err: fmt.Errorf("lstat pre-commit hook: %w", lstatErr)}
	}
	if lstatErr == nil && linfo.Mode()&os.ModeSymlink != 0 {
		streams.ErrPrintf("%s\n", ui.Warning(".git/hooks/pre-commit is a symlink; skipping modification to avoid breaking your hook manager. Add `smelt validate` to the hook manually."))
		return nil
	}

	content := string(existing)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		content += "\n"
	}
	content += brand.Command + " validate\n"
	info, statErr := os.Stat(path)
	if statErr != nil {
		return &userError{msg: "Could not stat .git/hooks/pre-commit.", suggestion: suggestionCheckPerms, err: fmt.Errorf("stat pre-commit hook: %w", statErr)}
	}
	if err := os.WriteFile(path, []byte(content), info.Mode()); err != nil {
		return &userError{
			msg:        "Could not update .git/hooks/pre-commit.",
			suggestion: suggestionCheckPerms,
			err:        fmt.Errorf("write pre-commit hook: %w", err),
		}
	}
	streams.ErrPrintln(ui.Success("Updated .git/hooks/pre-commit"))
	streams.ErrPrintln(ui.Dim("  Not tracked in git. Runs `smelt validate` locally before every commit you make."))
	return nil
}

// hasInstallCommand reports whether detection already produced an install step.
func hasInstallCommand(commands []config.Command) bool {
	return slices.ContainsFunc(commands, func(c config.Command) bool {
		return c.Name == config.CmdInstall
	})
}

// printDetectionSource says where the detected commands came from, and what
// detection could not read. Commands lifted from a CircleCI config can look
// nothing like the toolchain defaults, so the provenance is the difference
// between a surprising config and an explicable one.
func printDetectionSource(det validate.Detection, streams iostream.Streams) {
	if det.Source == "" {
		// Nothing was detected, so the notes are all detection has to say — and
		// they are what explains a CircleCI config that exists but could not be
		// used. Printed flush left, with no source line to sit under.
		for _, note := range det.Notes {
			streams.ErrPrintln(ui.Dim(note))
		}
		return
	}
	streams.ErrPrintf("Detected commands from %s\n", ui.Bold(det.Source))
	for _, note := range det.Notes {
		streams.ErrPrintln(ui.Dim("  " + note))
	}
}

// printInitSummary prints the discovered validation commands and next-step hints.
func printInitSummary(commands []config.Command, remoteHints bool, streams iostream.Streams) {
	if len(commands) > 0 {
		entries := make([]ui.CommandEntry, len(commands))
		for i, c := range commands {
			entries[i] = ui.CommandEntry{Name: c.Name, Run: c.Run}
		}
		streams.ErrPrintln("")
		streams.ErrPrintln(ui.Bold("Validation commands:"))
		streams.ErrPrintln(ui.CommandList(entries))
	}
	streams.ErrPrintln("")
	streams.ErrPrintf("Config: %s\n", ui.Bold(".smelt/config.json"))
	streams.ErrPrintf("  Edit to add, remove, or adjust commands.\n")
	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("Run validation:"))
	streams.ErrPrintf("  %-28s %s\n", ui.Cyan("smelt validate"), ui.Dim("run all commands locally"))
	if !remoteHints {
		return
	}
	streams.ErrPrintf("  %-28s %s\n", ui.Cyan("smelt validate --remote"), ui.Dim("run all commands on a remote sidecar"))
	for _, c := range commands {
		if c.Name == "install" {
			continue
		}
		streams.ErrPrintf("  %-28s %s\n", ui.Cyan("smelt validate --remote "+c.Name), ui.Dim("run "+c.Name+" remotely"))
	}
}

// detectOrgID attempts to resolve a CircleCI org ID without prompting for auth.
// Auto-selects when the user belongs to exactly one org; shows a picker when
// interactive with multiple orgs. Skipped gracefully on auth failure, no TTY,
// or cancellation — none of these are fatal for smelt init.
func detectOrgID(ctx context.Context, rc config.ResolvedConfig, streams iostream.Streams, cfg *config.ProjectConfig) {
	client, err := authprompt.ResolveCircleCIClient(rc, nil)
	if err != nil {
		if !errors.Is(err, authprompt.ErrNeedsAuth) {
			streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not detect org ID: %v", err)))
		}
		return
	}
	orgID, err := orgPicker(ctx, client, rc.CircleCITokenSource, streams)()
	if err != nil {
		if !errors.Is(err, ui.ErrNoTTY) && !errors.Is(err, ui.ErrCancelled) {
			warnUserError(streams, "Could not detect org ID: ", err)
		}
		return
	}
	cfg.OrgID = orgID
	streams.ErrPrintf("Org ID: %s\n", ui.Bold(orgID))
}

func detectVCS(ctx context.Context, workDir string, streams iostream.Streams, cfg *config.ProjectConfig) error {
	org, repo, err := gitremote.DetectOrgAndRepoCtx(ctx, workDir)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("detect VCS info: %w", ctxErr)
		}
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not detect VCS info: %v", err)))
		return nil
	}
	cfg.VCS = &config.VCSConfig{Org: org, Repo: repo}
	streams.ErrPrintf("Detected repository: %s\n", ui.Bold(fmt.Sprintf("%s/%s", org, repo)))
	return nil
}

// resolveGitCommonDir is the repository's shared git directory as an absolute
// path, or a user error when workDir is not in a git repository.
func resolveGitCommonDir(ctx context.Context, workDir string) (string, error) {
	git := gitexec.Runner{Dir: workDir}
	if err := git.Run(ctx, "rev-parse", "--git-dir"); err != nil {
		return "", &userError{msg: "Not a git repository.", suggestion: suggestionGitRepo, err: err}
	}
	out, err := git.Output(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", &userError{msg: "Could not determine git directory.", err: fmt.Errorf("git rev-parse --git-common-dir: %w", err)}
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(workDir, dir)
	}
	return dir, nil
}

// writeProjectConfig saves cfg as the project's .smelt/config.json and says so.
func writeProjectConfig(workDir string, cfg *config.ProjectConfig, streams iostream.Streams) error {
	if err := config.SaveProjectConfig(workDir, cfg); err != nil {
		return &userError{
			msg:        "Could not write .smelt/config.json.",
			suggestion: suggestionCheckPerms,
			err:        fmt.Errorf("write config: %w", err),
		}
	}
	streams.ErrPrintln(ui.Success("Wrote .smelt/config.json"))
	return nil
}

// detectInitCommands detects the project's validation commands and records them
// in cfg, keeping any timeout that was raised by hand.
func detectInitCommands(ctx context.Context, workDir string, streams iostream.Streams, rc config.ResolvedConfig, cfg *config.ProjectConfig) {
	claude, _ := anthropic.New(anthropic.Config{APIKey: rc.AnthropicAPIKey, BaseURL: rc.AnthropicBaseURL})
	det, err := validate.DetectCommands(ctx, claude, workDir)
	if err != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not detect commands: %v", err)))
		return
	}
	printDetectionSource(det, streams)
	allCommands := []config.Command{}
	// Detection may already have found an install command — a
	// CircleCI config names one directly. Only fall back to
	// guessing from the lock file when it did not.
	if !hasInstallCommand(det.Commands) {
		if pm := validate.DetectPackageManager(workDir); pm != nil {
			streams.ErrPrintf("Detected package manager: %s\n", ui.Bold(pm.Name))
			allCommands = append(allCommands, config.Command{Name: config.CmdInstall, Run: pm.InstallCommand})
		}
	}
	allCommands = append(allCommands, det.Commands...)
	// With --force, cfg still holds the previous commands: keep any
	// timeout that was raised beyond what detection suggests.
	cfg.Commands = config.KeepLongerTimeouts(allCommands, cfg.Commands)
	for _, c := range det.Commands {
		streams.ErrPrintf("Detected command: %s (%s)\n", ui.Bold(c.Name), ui.Gray(c.Run))
	}
}

func newInitCmd() *cobra.Command {
	var force, skipHooks, skipGitHook, skipValidate, skipCompletions, skipSkills, skipTestSuites, skipOrgID, skipAuth, skipFactory, skipImageBuild bool
	var projectDir, factoryBackend, factoryImage, factoryAuthor string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize project configuration",
		Long: `Set up .smelt/config.json with VCS and validate command configuration.

Detects VCS org/repo from git remote, detects test commands, and generates
hook config files.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			streams := iostream.FromCmd(cmd)
			ctx := cmd.Context()
			insecureStorage, _ := cmd.Flags().GetBool("insecure-storage")

			factoryOpts := initFactoryOptions{Backend: factoryBackend, Image: factoryImage, Author: factoryAuthor, Skip: skipFactory, SkipImageBuild: skipImageBuild}
			if err := factoryOpts.check(); err != nil {
				return err
			}

			workDir := projectDir
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return &userError{msg: msgCouldNotDetermineWorkDir, err: err}
				}
			}

			gitCommonDir, err := resolveGitCommonDir(ctx, workDir)
			if err != nil {
				return err
			}

			// Guard: exit cleanly if config exists and --force not set
			existingCfg, loadErr := config.LoadProjectConfig(workDir)
			if loadErr == nil && !force {
				hasData := existingCfg.HasCommands() || existingCfg.VCS != nil
				if hasData {
					streams.ErrPrintln("Config already exists at .smelt/config.json")
					streams.ErrPrintln(ui.Dim("To overwrite: smelt init --force"))
					if err := initFactoryRerun(ctx, workDir, streams, existingCfg, factoryOpts); err != nil {
						return err
					}
					// Still ensure pool state is gitignored for projects
					// initialized before this pattern existed.
					updateInitGitignore(workDir, streams)
					// Credentials are per user, so ensure the login anyway.
					earlyRC, _ := config.Resolve("", "", insecureStorage)
					claudeAuthStep(ctx, streams, earlyRC, insecureStorage, skipAuth, ui.Confirm)
					return nil
				}
			}

			// Seed from existing config when --force so skipped sections are preserved.
			cfg := &config.ProjectConfig{}
			if force && loadErr == nil {
				cfg = existingCfg
			}

			// Step 0: where factory runs execute. Asked before anything else
			// because it decides whether any CircleCI step below happens: an
			// authenticated machine alone must never reach CircleCI.
			factoryRes := initFactoryStep(ctx, workDir, streams, cfg, factoryOpts)
			plan := planInit(cfg.UsesCircleCI())

			// Step 1: VCS config from git remote
			if err := detectVCS(ctx, workDir, streams, cfg); err != nil {
				return err
			}

			rc, rcErr := config.Resolve("", "", insecureStorage)
			if rcErr != nil {
				streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not load config: %v", rcErr)))
			}

			// Step 2: Validate command detection
			if !skipValidate {
				detectInitCommands(ctx, workDir, streams, rc, cfg)
			}

			// Step 3: CircleCI org ID, for the circleci backend only
			if plan.DetectOrgID && !skipOrgID && cfg.OrgID == "" {
				detectOrgID(ctx, rc, streams, cfg)
			}

			// Step 3b: the docker image, now that the commands it is verified
			// against are known
			if factoryRes.BackendChosen {
				initManagedImage(ctx, workDir, streams, cfg, factoryOpts)
			}

			if err := writeProjectConfig(workDir, cfg, streams); err != nil {
				return err
			}

			updateInitGitignore(workDir, streams)

			// Step 4: Write hook config files for supported agents.
			if !skipHooks {
				if err := writeAllHookFiles(workDir, cfg.Commands, streams); err != nil {
					return err
				}
				if !skipGitHook {
					if err := writeGitHook(gitCommonDir, streams); err != nil {
						return err
					}
				}
			}

			// Step 5: Shell completions
			if !skipCompletions {
				maybeInstallCompletions(streams)
			}

			// Step 6: CircleCI Smarter Testing test-suites.yml, circleci only
			if plan.TestSuites {
				if skipTestSuites {
					printTestSuitesHint(workDir, streams)
				} else if err := writeTestSuites(workDir, streams); err != nil {
					streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not write .circleci/test-suites.yml: %v", err)))
				}
			}

			// Step 7: Agent skills
			if !skipSkills {
				installSkillsStep(workDir, streams, plan.Skills)
			}

			// Step 8: Claude credential. Re-resolve: earlier steps may not have
			// changed it, but this keeps the report accurate.
			authRC, _ := config.Resolve("", "", insecureStorage)
			claudeAuthStep(ctx, streams, authRC, insecureStorage, skipAuth, ui.Confirm)

			streams.ErrPrintln(ui.Success("Project initialized"))
			printInitSummary(cfg.Commands, plan.RemoteHints, streams)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing config")
	cmd.Flags().BoolVar(&skipHooks, "skip-hooks", false, "Skip hook file generation")
	cmd.Flags().BoolVar(&skipGitHook, "skip-git-hook", false, "Skip git pre-commit hook installation")
	cmd.Flags().BoolVar(&skipValidate, "skip-validate", false, "Skip validate command detection")
	cmd.Flags().BoolVar(&skipCompletions, "skip-completions", false, "Skip shell completion installation")
	cmd.Flags().BoolVar(&skipSkills, "skip-skills", false, "Skip agent skill installation")
	cmd.Flags().BoolVar(&skipTestSuites, "skip-test-suites", true, "Skip CircleCI test-suites.yml generation (circleci backend only; default: skip; pass =false to use built-in Go/pytest templates)")
	cmd.Flags().BoolVar(&skipOrgID, "skip-org-id", false, "Skip CircleCI org ID detection (circleci backend only)")
	cmd.Flags().BoolVar(&skipAuth, "skip-auth", false, "Skip the Claude login check")
	cmd.Flags().BoolVar(&skipFactory, "skip-factory", false, "Skip the factory backend step")
	cmd.Flags().BoolVar(&skipImageBuild, "skip-image-build", false, "Write Dockerfile.smelt for docker factory runs but do not offer to build the image")
	cmd.Flags().StringVar(&factoryBackend, "factory-backend", "", "Where factory runs execute: docker (default) or circleci (skips the prompt)")
	cmd.Flags().StringVar(&factoryImage, "factory-image", "", "Docker image for factory runs (skips the prompt)")
	cmd.Flags().StringVar(&factoryAuthor, "factory-author", "", `Who factory commits are attributed to, as "Name <email>" (default: your git identity)`)
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "Project directory (defaults to current directory)")

	return cmd
}
