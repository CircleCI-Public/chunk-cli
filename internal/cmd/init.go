package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/hook"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
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

func newInitCmd() *cobra.Command {
	var force, skipHooks, skipValidate, skipCircleCI bool
	var profile, projectDir string

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

			if err := hook.ValidateProfile(profile); err != nil {
				return err
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

			// Step 4: Hook setup
			if !skipHooks {
				projectName := ""
				if cfg.VCS != nil && cfg.VCS.Repo != "" {
					projectName = cfg.VCS.Repo
				}
				if err := hook.RunSetup(workDir, projectName, profile, force, false, "", cfg.Commands, streams); err != nil {
					return fmt.Errorf("hook setup: %w", err)
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
	cmd.Flags().StringVar(&profile, "profile", "enable",
		fmt.Sprintf("Shell environment profile (%s)", strings.Join(hook.ValidProfiles, ", ")))

	return cmd
}
