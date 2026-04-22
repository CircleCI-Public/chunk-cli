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
func writeSettings(workDir string, commands []config.Command, streams iostream.Streams, confirm confirmFunc) error {
	generated, err := settings.Build(commands)
	if err != nil {
		return &userError{msg: "Could not build .claude/settings.json.", err: fmt.Errorf("build settings: %w", err)}
	}

	dir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &userError{
			msg:        "Could not create .claude directory.",
			suggestion: "Check file permissions.",
			err:        fmt.Errorf("create .claude dir: %w", err),
		}
	}

	path := filepath.Join(dir, "settings.json")
	existing, readErr := os.ReadFile(path)
	if readErr != nil {
		if !errors.Is(readErr, fs.ErrNotExist) {
			return &userError{
				msg:        "Could not read .claude/settings.json.",
				suggestion: "Check file permissions.",
				err:        fmt.Errorf("read existing settings.json: %w", readErr),
			}
		}
		// No existing file — write directly.
		if err := os.WriteFile(path, withTrailingNewline(generated), 0o644); err != nil {
			return &userError{
				msg:        "Could not write .claude/settings.json.",
				suggestion: "Check file permissions.",
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
	}
	if confirmErr != nil || !apply {
		// Decline, cancel, or non-TTY — fall back to example file.
		return writeSettingsExample(dir, generated, streams)
	}

	if err := os.WriteFile(path, withTrailingNewline(result.Merged), 0o644); err != nil {
		return &userError{
			msg:        "Could not write .claude/settings.json.",
			suggestion: "Check file permissions.",
			err:        fmt.Errorf("write settings.json: %w", err),
		}
	}
	streams.ErrPrintln(ui.Success("Updated .claude/settings.json"))
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

var sandboxGitignoreEntries = []string{
	".chunk/sandbox.json",
	".chunk/sandbox.*.json",
}

// ensureGitignoreEntries appends sandbox tracking patterns to .gitignore if
// they are not already present.
func ensureGitignoreEntries(workDir string, streams iostream.Streams) error {
	path := filepath.Join(workDir, ".gitignore")

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read .gitignore: %w", err)
	}

	content := string(existing)
	var toAdd []string
	for _, entry := range sandboxGitignoreEntries {
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

	if _, err := f.WriteString("\n# chunk active sandbox tracking\n"); err != nil {
		return err
	}
	for _, entry := range toAdd {
		if _, err := f.WriteString(entry + "\n"); err != nil {
			return err
		}
	}

	streams.ErrPrintln(ui.Success("Updated .gitignore with sandbox tracking patterns"))
	return nil
}

func newInitCmd() *cobra.Command {
	var force, skipHooks, skipValidate, skipCompletions bool
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

			workDir := projectDir
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return &userError{msg: "Could not determine working directory.", err: err}
				}
			}

			gitCmd := exec.Command("git", "rev-parse", "--git-dir")
			gitCmd.Dir = workDir
			if err := gitCmd.Run(); err != nil {
				return &userError{msg: "Not a git repository.", suggestion: "Run this command from inside a git repo.", err: err}
			}

			// Guard: exit cleanly if config exists and --force not set
			existingCfg, loadErr := config.LoadProjectConfig(workDir)
			if loadErr == nil && !force {
				hasData := existingCfg.HasCommands() || existingCfg.VCS != nil
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
				rc, _ := config.Resolve("", "")
				claude, _ := anthropic.New(anthropic.Config{APIKey: rc.AnthropicAPIKey, BaseURL: rc.AnthropicBaseURL})
				commands, detectErr := validate.DetectCommands(ctx, claude, workDir)
				if detectErr != nil {
					streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not detect commands: %v", detectErr)))
				} else {
					allCommands := []config.Command{}
					pm := validate.DetectPackageManager(workDir)
					if pm != nil {
						streams.ErrPrintf("Detected package manager: %s\n", ui.Bold(pm.Name))
						allCommands = append(allCommands, config.Command{Name: "install", Run: pm.InstallCommand})
					}
					allCommands = append(allCommands, commands...)
					cfg.Commands = allCommands
					for _, c := range commands {
						streams.ErrPrintf("Detected command: %s (%s)\n", ui.Bold(c.Name), ui.Gray(c.Run))
					}
				}
			}

			// Save config
			if err := config.SaveProjectConfig(workDir, cfg); err != nil {
				return &userError{
					msg:        "Could not write .chunk/config.json.",
					suggestion: "Check file permissions.",
					err:        fmt.Errorf("write config: %w", err),
				}
			}
			streams.ErrPrintln(ui.Success("Wrote .chunk/config.json"))

			if err := ensureGitignoreEntries(workDir, streams); err != nil {
				streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not update .gitignore: %v", err)))
			}

			// Step 3: Write .claude/settings.json
			if !skipHooks {
				if err := writeSettings(workDir, cfg.Commands, streams, tui.Confirm); err != nil {
					return err
				}
			}

			// Step 4: Shell completions
			if !skipCompletions {
				installed, err := completionInstalled()
				if err != nil {
					streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Skipping shell completions: %v", err)))
				} else if !installed {
					yes, confirmErr := tui.Confirm("Install shell completions?", true)
					if confirmErr != nil {
						streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not confirm: %v", confirmErr)))
					} else if yes {
						if installErr := installCompletion(streams); installErr != nil {
							streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not install completions: %v", installErr)))
						}
					}
				}
			}

			streams.ErrPrintln(ui.Success("Project initialized"))
			streams.ErrPrintln(ui.Dim("Tip: install agent skills with 'chunk skill install' (includes chunk-sandbox for the remote validate loop)."))
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing config")
	cmd.Flags().BoolVar(&skipHooks, "skip-hooks", false, "Skip hook file generation")
	cmd.Flags().BoolVar(&skipValidate, "skip-validate", false, "Skip validate command detection")
	cmd.Flags().BoolVar(&skipCompletions, "skip-completions", false, "Skip shell completion installation")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "Project directory (defaults to current directory)")

	return cmd
}
