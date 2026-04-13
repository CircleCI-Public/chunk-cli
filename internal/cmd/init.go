package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/settings"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

// pickCircleCIOrg prompts the user to select a CircleCI organization.
// Returns the selected org ID and name, or empty strings if selection is skipped.
func pickCircleCIOrg(ctx context.Context, streams iostream.Streams) (orgID, orgName string) {
	client, err := circleci.NewClient()
	if err != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Skipping CircleCI setup: %v", err)))
		return "", ""
	}

	streams.ErrPrintln(ui.Dim("Fetching CircleCI organizations..."))
	collabs, err := client.ListCollaborations(ctx)
	if err != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not fetch organizations: %v", err)))
		return "", ""
	}
	if len(collabs) == 0 {
		streams.ErrPrintln(ui.Warning("No CircleCI organizations found"))
		return "", ""
	}

	if len(collabs) == 1 {
		return collabs[0].ID, collabs[0].Name
	}

	items := make([]string, len(collabs))
	for i, c := range collabs {
		items[i] = c.Name
	}
	idx, err := tui.SelectFromList("Select CircleCI organization:", items)
	if err != nil {
		streams.ErrPrintln(ui.Warning("Skipping CircleCI org selection"))
		return "", ""
	}
	return collabs[idx].ID, collabs[idx].Name
}

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
		return fmt.Errorf("build settings: %w", err)
	}

	dir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create .claude dir: %w", err)
	}

	path := filepath.Join(dir, "settings.json")
	existing, readErr := os.ReadFile(path)
	if readErr != nil {
		if !errors.Is(readErr, fs.ErrNotExist) {
			return fmt.Errorf("read existing settings.json: %w", readErr)
		}
		// No existing file — write directly.
		if err := os.WriteFile(path, withTrailingNewline(generated), 0o644); err != nil {
			return fmt.Errorf("write settings.json: %w", err)
		}
		streams.ErrPrintln(ui.Success("Wrote .claude/settings.json"))
		return nil
	}

	// Existing file found — compute merge.
	result, err := settings.Merge(existing, generated)
	if err != nil {
		return fmt.Errorf("merge settings: %w", err)
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
		return fmt.Errorf("write settings.json: %w", err)
	}
	streams.ErrPrintln(ui.Success("Updated .claude/settings.json"))
	return nil
}

// writeSettingsExample writes settings.example.json as a fallback.
func writeSettingsExample(dir string, data []byte, streams iostream.Streams) error {
	exPath := filepath.Join(dir, "settings.example.json")
	if err := os.WriteFile(exPath, withTrailingNewline(data), 0o644); err != nil {
		return fmt.Errorf("write settings.example.json: %w", err)
	}
	streams.ErrPrintln(ui.Success("Wrote .claude/settings.example.json (existing settings.json preserved)"))
	return nil
}

func newInitCmd() *cobra.Command {
	var force, skipHooks, skipValidate, skipCircleCI bool
	var projectDir string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize project configuration",
		Long: `Set up .chunk/config.json with VCS, CircleCI, and validate command configuration.

Detects VCS org/repo from git remote, prompts for CircleCI org, detects test
commands, and generates hook config files.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			streams := iostream.FromCmd(cmd)
			ctx := cmd.Context()

			workDir := projectDir
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return err
				}
			}

			gitCmd := exec.Command("git", "rev-parse", "--git-dir")
			gitCmd.Dir = workDir
			if err := gitCmd.Run(); err != nil {
				return fmt.Errorf("not a git repository, run this command from inside a git repo")
			}

			// Guard: exit cleanly if config exists and --force not set
			existingCfg, loadErr := config.LoadProjectConfig(workDir)
			if loadErr == nil && !force {
				hasData := existingCfg.HasCommands() || existingCfg.VCS != nil || existingCfg.CircleCI != nil
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

			// Step 2: CircleCI org picker
			if !skipCircleCI {
				if orgID, orgName := pickCircleCIOrg(ctx, streams); orgID != "" {
					cfg.CircleCI = &config.CircleCIConfig{OrgID: orgID}
					streams.ErrPrintf("Selected organization: %s\n", ui.Bold(orgName))
				}
			}

			// Step 3: Validate command detection
			if !skipValidate {
				claude, _ := anthropic.New() // nil if unavailable — static detection works without it
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
				return fmt.Errorf("write config: %w", err)
			}
			streams.ErrPrintln(ui.Success("Wrote .chunk/config.json"))

			// Step 4: Write .claude/settings.json
			if !skipHooks {
				if err := writeSettings(workDir, cfg.Commands, streams, tui.Confirm); err != nil {
					return fmt.Errorf("settings: %w", err)
				}
			}

			streams.ErrPrintln(ui.Success("Project initialized"))
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing config")
	cmd.Flags().BoolVar(&skipHooks, "skip-hooks", false, "Skip hook file generation")
	cmd.Flags().BoolVar(&skipValidate, "skip-validate", false, "Skip validate command detection")
	cmd.Flags().BoolVar(&skipCircleCI, "skip-circleci", false, "Skip CircleCI org picker")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "Project directory (defaults to current directory)")

	return cmd
}
