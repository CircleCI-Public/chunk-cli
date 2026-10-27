package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/settings"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// confirmFunc asks the user a yes/no question. Matches ui.Confirm signature.
type confirmFunc func(label string, defaultYes bool) (bool, error)

// initStatus renders init's progress the way chunk init always has: flush
// left, with explanations dimmed beneath what they explain.
func initStatus(streams iostream.Streams) iostream.StatusFunc {
	return func(level iostream.Level, msg string) {
		switch level {
		case iostream.LevelStep:
			streams.ErrPrintln(msg)
		case iostream.LevelInfo:
			streams.ErrPrintln(ui.Dim("  " + msg))
		case iostream.LevelWarn:
			streams.ErrPrintln(ui.Warning(msg))
		case iostream.LevelDone:
			streams.ErrPrintln(ui.Success(msg))
		case iostream.LevelError:
			streams.ErrPrintln(ui.ErrError(msg))
		}
	}
}

// confirmDiff shows the change init would make to a file and asks
// whether to make it.
func confirmDiff(streams iostream.Streams, confirm confirmFunc) settings.ConfirmFunc {
	return func(path, diff string) (bool, error) {
		streams.ErrPrintln("")
		streams.ErrPrintln(ui.Bold("Changes to " + path + ":"))
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
		return confirm("Apply changes to "+path+"?", false)
	}
}

// gitDirError phrases an error from finding the project's git directory.
func gitDirError(err error) error {
	if errors.Is(err, gitutil.ErrNotGitRepo) {
		return &userError{msg: "Not a git repository.", suggestion: suggestionGitRepo, err: err}
	}
	return &userError{msg: "Could not determine git directory.", err: err}
}

// printInitSummary prints the discovered validation commands and next-step hints.
func printInitSummary(commands []config.Command, streams iostream.Streams) {
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
	streams.ErrPrintf("Config: %s\n", ui.Bold(".chunk/config.json"))
	streams.ErrPrintf("  Edit to add, remove, or adjust commands.\n")
	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("Run validation:"))
	streams.ErrPrintf("  %-28s %s\n", ui.Cyan("chunk validate"), ui.Dim("run all commands locally"))
	streams.ErrPrintf("  %-28s %s\n", ui.Cyan("chunk validate --remote"), ui.Dim("run all commands on a remote sidecar"))
	for _, c := range commands {
		if c.Name == "install" {
			continue
		}
		streams.ErrPrintf("  %-28s %s\n", ui.Cyan("chunk validate --remote "+c.Name), ui.Dim("run "+c.Name+" remotely"))
	}
	if showHints(streams, false) {
		streams.ErrPrintln("")
		streams.ErrPrintf("Keep %s open in another terminal to stay on top of your runs.\n", ui.Cyan("chunk ui"))
	}
}

// detectOrgID attempts to resolve a CircleCI org ID without prompting for auth.
// Auto-selects when the user belongs to exactly one org; shows a picker when
// interactive with multiple orgs. Returns "" on auth failure, no TTY, or
// cancellation — none of these are fatal for chunk init — warning about any
// other failure.
func detectOrgID(ctx context.Context, rc config.ResolvedConfig, streams iostream.Streams) string {
	client, err := authprompt.ResolveCircleCIClient(rc, nil)
	if err != nil {
		if !errors.Is(err, authprompt.ErrNeedsAuth) {
			streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not detect org ID: %v", err)))
		}
		return ""
	}
	orgID, err := orgPicker(ctx, client, rc.CircleCITokenSource, streams)()
	if err != nil {
		if !errors.Is(err, ui.ErrNoTTY) && !errors.Is(err, ui.ErrCancelled) {
			warnUserError(streams, "Could not detect org ID: ", err)
		}
		return ""
	}
	return orgID
}

// initOptions chooses which steps of chunk init run.
type initOptions struct {
	force           bool
	skipHooks       bool
	skipGitHook     bool
	skipValidate    bool
	skipCompletions bool
	skipSkills      bool
	skipTestSuites  bool
	skipOrgID       bool
	insecureStorage bool
}

// runInit initializes workDir. It leaves an existing config alone unless
// opts.force is set, and then keeps whatever the skipped steps would have
// replaced.
func runInit(ctx context.Context, workDir string, opts initOptions, streams iostream.Streams) error {
	status := initStatus(streams)
	gitCommonDir, err := gitutil.CommonDirCtx(ctx, workDir)
	if err != nil {
		return gitDirError(err)
	}

	// Guard: exit cleanly if config exists and --force not set
	existingCfg, loadErr := config.LoadProjectConfig(workDir)
	if loadErr == nil && !opts.force {
		hasData := existingCfg.HasCommands() || existingCfg.VCS != nil
		if hasData {
			streams.ErrPrintln("Config already exists at .chunk/config.json")
			streams.ErrPrintln(ui.Dim("To overwrite: chunk init --force"))
			// Still ensure pool state is gitignored for projects
			// initialized before this pattern existed.
			updateGitignore(workDir, status)
			return nil
		}
	}

	// Seed from existing config when --force so skipped sections are preserved.
	cfg := &config.ProjectConfig{}
	if opts.force && loadErr == nil {
		cfg = existingCfg
	}

	rc, rcErr := config.Resolve("", "", opts.insecureStorage)
	if rcErr != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not load config: %v", rcErr))
	}
	vcs, err := detectVCS(ctx, workDir, status)
	if err != nil {
		return err
	}
	if vcs != nil {
		cfg.VCS = vcs
	}
	if !opts.skipValidate {
		claude, _ := anthropic.New(anthropic.Config{APIKey: rc.AnthropicAPIKey, BaseURL: rc.AnthropicBaseURL})
		if commands, ok := detectCommands(ctx, claude, workDir, status); ok {
			cfg.Commands = commands
		}
	}
	if !opts.skipOrgID && cfg.OrgID == "" {
		if orgID := detectOrgID(ctx, rc, streams); orgID != "" {
			cfg.OrgID = orgID
			status(iostream.LevelStep, "Org ID: "+ui.Bold(orgID))
		}
	}

	if err := config.SaveProjectConfig(workDir, cfg); err != nil {
		return &userError{
			msg:        "Could not write .chunk/config.json.",
			suggestion: suggestionCheckPerms,
			err:        fmt.Errorf("write config: %w", err),
		}
	}
	status(iostream.LevelDone, "Wrote .chunk/config.json")

	updateGitignore(workDir, status)
	if !opts.skipHooks {
		confirm := confirmDiff(streams, ui.Confirm)
		if err := writeAgentHooks(workDir, os.Getenv(config.EnvHome), cfg.Commands, status, confirm); err != nil {
			return err
		}
		if !opts.skipGitHook {
			if err := writeGitHook(gitCommonDir, status); err != nil {
				return err
			}
		}
	}
	if !opts.skipCompletions {
		maybeInstallCompletions(streams)
	}
	if opts.skipTestSuites {
		sidecarHint(workDir, streams)
	} else {
		writeTestSuites(workDir, status)
	}
	if !opts.skipSkills {
		installSkills(workDir, status)
	}

	status(iostream.LevelDone, "Project initialized")
	printInitSummary(cfg.Commands, streams)
	return nil
}

func newInitCmd() *cobra.Command {
	var opts initOptions
	var projectDir string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize project configuration",
		Long: `Set up .chunk/config.json with VCS and validate command configuration.

Detects VCS org/repo from git remote, detects test commands, and generates
hook config files.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			workDir := projectDir
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return &userError{msg: msgCouldNotDetermineWorkDir, err: err}
				}
			}
			opts.insecureStorage, _ = cmd.Flags().GetBool("insecure-storage")
			return runInit(cmd.Context(), workDir, opts, iostream.FromCmd(cmd))
		},
	}

	cmd.Flags().BoolVar(&opts.force, "force", false, "Overwrite existing config")
	cmd.Flags().BoolVar(&opts.skipHooks, "skip-hooks", false, "Skip hook file generation")
	cmd.Flags().BoolVar(&opts.skipGitHook, "skip-git-hook", false, "Skip git pre-commit hook installation")
	cmd.Flags().BoolVar(&opts.skipValidate, "skip-validate", false, "Skip validate command detection")
	cmd.Flags().BoolVar(&opts.skipCompletions, "skip-completions", false, "Skip shell completion installation")
	cmd.Flags().BoolVar(&opts.skipSkills, "skip-skills", false, "Skip agent skill installation")
	cmd.Flags().BoolVar(&opts.skipTestSuites, "skip-test-suites", true, "Skip CircleCI test-suites.yml generation (default: skip; pass =false to use built-in Go/pytest templates)")
	cmd.Flags().BoolVar(&opts.skipOrgID, "skip-org-id", false, "Skip CircleCI org ID detection")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "Project directory (defaults to current directory)")

	return cmd
}
