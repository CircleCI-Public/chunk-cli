package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/hook"
	"github.com/CircleCI-Public/chunk-cli/internal/initialize"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newInitCmd() *cobra.Command {
	var force, skipHooks, skipValidate, skipCircleCI, skipSkills bool
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

			opts := initialize.Options{
				WorkDir:      workDir,
				HomeDir:      os.Getenv("HOME"),
				Profile:      profile,
				Force:        force,
				SkipHooks:    skipHooks,
				SkipValidate: skipValidate,
				SkipCircleCI: skipCircleCI,
				SkipSkills:   skipSkills,
			}

			result, err := initialize.Run(ctx, opts, streams)
			if err != nil {
				return err
			}
			if result == nil {
				return nil // already-initialized guard printed a message
			}

			// Print skill install results
			for _, r := range result.SkillResults {
				if r.Skipped {
					streams.ErrPrintln(ui.Dim(r.Agent + ": skipped (not installed)"))
					continue
				}
				if len(r.Installed) == 0 && len(r.Updated) == 0 {
					streams.ErrPrintln(r.Agent + ": " + ui.Green("all skills up to date"))
					continue
				}
				for _, name := range r.Installed {
					streams.ErrPrintln(r.Agent + ": " + ui.Green("installed "+name))
				}
				for _, name := range r.Updated {
					streams.ErrPrintln(r.Agent + ": " + ui.Yellow("updated "+name))
				}
			}

			// Summary
			streams.ErrPrintln(ui.Success("Project initialized"))
			streams.ErrPrintln("")
			streams.ErrPrintln("  What was set up:")
			if result.Org != "" && result.Repo != "" {
				streams.ErrPrintf("    VCS:       %s\n", result.Org+"/"+result.Repo)
			}
			if result.CircleCIOrgName != "" {
				streams.ErrPrintf("    CircleCI:  %s\n", result.CircleCIOrgName)
			}
			if len(result.Commands) > 0 {
				names := make([]string, len(result.Commands))
				for i, c := range result.Commands {
					names[i] = c.Name
				}
				streams.ErrPrintf("    Commands:  %s\n", strings.Join(names, ", "))
			}
			if result.HooksSetUp {
				streams.ErrPrintln("    Hooks:     configured")
			}
			if len(result.SkillResults) > 0 {
				var installed []string
				for _, r := range result.SkillResults {
					if !r.Skipped && (len(r.Installed) > 0 || len(r.Updated) > 0) {
						installed = append(installed, r.Agent)
					}
				}
				if len(installed) > 0 {
					streams.ErrPrintf("    Skills:    installed for %s\n", strings.Join(installed, ", "))
				}
			}
			streams.ErrPrintln("")
			streams.ErrPrintln("  Next steps:")
			streams.ErrPrintln("    chunk validate --list    Review detected commands")
			streams.ErrPrintln("    chunk build-prompt       Generate a review prompt")
			streams.ErrPrintln("    chunk skills list        Check skill installation status")
			streams.ErrPrintln("")
			streams.ErrPrintln(ui.Dim("  Tip: Detected commands are starting points — verify they match your project."))

			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing config")
	cmd.Flags().BoolVar(&skipHooks, "skip-hooks", false, "Skip hook file generation")
	cmd.Flags().BoolVar(&skipValidate, "skip-validate", false, "Skip validate command detection")
	cmd.Flags().BoolVar(&skipCircleCI, "skip-circleci", false, "Skip CircleCI org picker")
	cmd.Flags().BoolVar(&skipSkills, "skip-skills", false, "Skip skill installation")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "Project directory (defaults to current directory)")
	cmd.Flags().StringVar(&profile, "profile", "enable",
		fmt.Sprintf("Shell environment profile (%s)", strings.Join(hook.ValidProfiles, ", ")))

	return cmd
}
