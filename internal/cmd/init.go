package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/settings"
	"github.com/CircleCI-Public/chunk-cli/internal/skills"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

// confirmFunc asks the user a yes/no question. Matches tui.Confirm signature.
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
func writeSettings(workDir string, cfg *config.ProjectConfig, streams iostream.Streams, confirm confirmFunc) error {
	generated, err := settings.Build(cfg.Fix)
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
func writeCodexHooks(workDir string, cfg *config.ProjectConfig, streams iostream.Streams, confirm confirmFunc) error {
	generated, err := settings.BuildCodex(cfg.Fix)
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

func installSkillsStep(workDir string, streams iostream.Streams) {
	for _, r := range skills.InstallByName(skills.ScopeProject, workDir, "chunk-sidecar") {
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
	streams.ErrPrintln(ui.Dim("  Ask your AI coding agent to run the chunk-sidecar skill to spin up"))
	streams.ErrPrintln(ui.Dim("  a microVM, sync your repo, and run tests remotely."))
}

// writeAllHookFiles writes hook config files for all supported agents.
// Cursor reads .claude/settings.json natively so no extra file is needed for it.
// Codex hooks are only written when Codex is installed or the project already
// has a .codex directory.
func writeAllHookFiles(workDir string, cfg *config.ProjectConfig, streams iostream.Streams) error {
	if err := writeSettings(workDir, cfg, streams, tui.Confirm); err != nil {
		return err
	}
	homeDir := os.Getenv(config.EnvHome)
	_, codexDirErr := os.Stat(filepath.Join(workDir, ".codex"))
	if codexInstalled(homeDir) || codexDirErr == nil {
		if err := writeCodexHooks(workDir, cfg, streams, tui.Confirm); err != nil {
			return err
		}
	}
	return nil
}

// writePrePushHook installs the pre-push git hook for the project at workDir.
func writePrePushHook(workDir string, streams iostream.Streams) error {
	hookPath, err := prePushHookPath(workDir)
	if err != nil {
		return &userError{
			msg: "Could not locate git hooks directory.",
			err: fmt.Errorf("locate pre-push hook path: %w", err),
		}
	}
	if err := installPrePushHook(hookPath, streams); err != nil {
		return &userError{
			msg:        "Could not install .git/hooks/pre-push.",
			suggestion: suggestionCheckPerms,
			err:        err,
		}
	}
	return nil
}

// printInitSummary prints the discovered commands and next-step hints.
func printInitSummary(cfg *config.ProjectConfig, streams iostream.Streams) {
	if len(cfg.Validate) > 0 {
		entries := make([]ui.CommandEntry, len(cfg.Validate))
		for i, c := range cfg.Validate {
			entries[i] = ui.CommandEntry{Name: c.Name, Run: c.Run}
		}
		streams.ErrPrintln("")
		streams.ErrPrintln(ui.Bold("Validate commands:"))
		streams.ErrPrintln(ui.CommandList(entries))
	}
	if len(cfg.Fix) > 0 {
		entries := make([]ui.CommandEntry, len(cfg.Fix))
		for i, c := range cfg.Fix {
			entries[i] = ui.CommandEntry{Name: c.Name, Run: c.Run}
		}
		streams.ErrPrintln("")
		streams.ErrPrintln(ui.Bold("Fix commands (run after every file edit):"))
		streams.ErrPrintln(ui.CommandList(entries))
	}
	streams.ErrPrintln("")
	streams.ErrPrintf("Config: %s\n", ui.Bold(".chunk/config.json"))
	streams.ErrPrintf("  Edit to add, remove, or adjust commands.\n")
	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("Run commands:"))
	streams.ErrPrintf("  %-28s %s\n", ui.Cyan("chunk validate"), ui.Dim("run all validate commands locally"))
	streams.ErrPrintf("  %-28s %s\n", ui.Cyan("chunk fix"), ui.Dim("run all fix commands locally"))
	streams.ErrPrintf("  %-28s %s\n", ui.Cyan("chunk validate --remote"), ui.Dim("run all validate commands on a remote sidecar"))
	for _, c := range cfg.Validate {
		if c.Name == "install" {
			continue
		}
		streams.ErrPrintf("  %-28s %s\n", ui.Cyan("chunk validate --remote "+c.Name), ui.Dim("run "+c.Name+" remotely"))
	}
}

func newInitCmd() *cobra.Command {
	var force, skipHooks, skipGitHook, skipValidate, skipCompletions, skipSkills, skipTestSuites bool
	var projectDir string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize project configuration",
		Long: `Set up .chunk/config.json with VCS and validate command configuration.

Detects VCS org/repo from git remote, detects test commands, and generates
hook config files.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			streams := iostream.FromCmd(cmd)
			ctx := cmd.Context()
			insecureStorage, _ := cmd.Flags().GetBool("insecure-storage")

			workDir := projectDir
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return &userError{msg: msgCouldNotDetermineWorkDir, err: err}
				}
			}

			gitCmd := exec.Command("git", "rev-parse", "--git-dir")
			gitCmd.Dir = workDir
			if err := gitCmd.Run(); err != nil {
				return &userError{msg: "Not a git repository.", suggestion: suggestionGitRepo, err: err}
			}

			// Guard: exit cleanly if config exists and --force not set
			existingCfg, loadErr := config.LoadProjectConfig(workDir)
			if loadErr == nil && !force {
				hasData := existingCfg.HasCommands() || existingCfg.VCS != nil // HasCommands checks both fix and validate
				if hasData {
					streams.ErrPrintln("Config already exists at .chunk/config.json")
					streams.ErrPrintln(ui.Dim("To overwrite: chunk init --force"))
					return nil
				}
			}

			// Seed from existing config when --force so skipped sections are preserved.
			cfg := &config.ProjectConfig{}
			if force && loadErr == nil {
				cfg = existingCfg
			}

			// Step 1: VCS config from git remote
			org, repo, err := gitremote.DetectOrgAndRepo(workDir)
			if err != nil {
				streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not detect VCS info: %v", err)))
			} else {
				cfg.VCS = &config.VCSConfig{Org: org, Repo: repo}
				streams.ErrPrintf("Detected repository: %s\n", ui.Bold(fmt.Sprintf("%s/%s", org, repo)))
			}

			// Step 2: Validate command detection
			if !skipValidate {
				rc, _ := config.Resolve("", "", insecureStorage)
				claude, _ := anthropic.New(anthropic.Config{APIKey: rc.AnthropicAPIKey, BaseURL: rc.AnthropicBaseURL})
				detected, detectErr := validate.DetectCommands(ctx, claude, workDir)
				if detectErr != nil {
					streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not detect commands: %v", detectErr)))
				} else if detected != nil {
					pm := validate.DetectPackageManager(workDir)
					if pm != nil {
						streams.ErrPrintf("Detected package manager: %s\n", ui.Bold(pm.Name))
						cfg.Validate = append([]config.ValidateCommand{{Name: "install", Run: pm.InstallCommand}}, detected.Validate...)
					} else {
						cfg.Validate = detected.Validate
					}
					cfg.Fix = detected.Fix
					for _, c := range detected.Validate {
						streams.ErrPrintf("Detected command: %s (%s)\n", ui.Bold(c.Name), ui.Gray(c.Run))
					}
					for _, c := range detected.Fix {
						streams.ErrPrintf("Detected fix command: %s (%s)\n", ui.Bold(c.Name), ui.Gray(c.Run))
					}
				}
			}

			// Save config
			if err := config.SaveProjectConfig(workDir, cfg); err != nil {
				return &userError{
					msg:        "Could not write .chunk/config.json.",
					suggestion: suggestionCheckPerms,
					err:        fmt.Errorf("write config: %w", err),
				}
			}
			streams.ErrPrintln(ui.Success("Wrote .chunk/config.json"))

			// Step 3: Write hook config files for supported agents.
			if !skipHooks {
				if err := writeAllHookFiles(workDir, cfg, streams); err != nil {
					return err
				}
				if !skipGitHook {
					if err := writePrePushHook(workDir, streams); err != nil {
						return err
					}
				}
			}

			// Step 4: Shell completions
			if !skipCompletions {
				maybeInstallCompletions(streams)
			}

			// Step 5: CircleCI Smarter Testing test-suites.yml
			if skipTestSuites {
				printTestSuitesHint(workDir, streams)
			} else {
				if err := writeTestSuites(workDir, streams); err != nil {
					streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not write .circleci/test-suites.yml: %v", err)))
				}
			}

			// Step 6: Agent skills
			if !skipSkills {
				installSkillsStep(workDir, streams)
			}

			streams.ErrPrintln(ui.Success("Project initialized"))
			printInitSummary(cfg, streams)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing config")
	cmd.Flags().BoolVar(&skipHooks, "skip-hooks", false, "Skip hook file generation")
	cmd.Flags().BoolVar(&skipGitHook, "skip-git-hook", false, "Skip git pre-push hook installation")
	cmd.Flags().BoolVar(&skipValidate, "skip-validate", false, "Skip validate command detection")
	cmd.Flags().BoolVar(&skipCompletions, "skip-completions", false, "Skip shell completion installation")
	cmd.Flags().BoolVar(&skipSkills, "skip-skills", false, "Skip agent skill installation")
	cmd.Flags().BoolVar(&skipTestSuites, "skip-test-suites", true, "Skip CircleCI test-suites.yml generation (default: skip; pass =false to use built-in Go/pytest templates)")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "Project directory (defaults to current directory)")

	return cmd
}
