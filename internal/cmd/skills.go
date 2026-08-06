package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/skills"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "skill",
		Short:              "Install and manage AI agent skills",
		RunE:               groupRunE,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	}

	cmd.AddCommand(newSkillInstallCmd())
	cmd.AddCommand(newSkillListCmd())
	return cmd
}

func newSkillInstallCmd() *cobra.Command {
	var jsonOut bool
	var scopeFlag string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install or update all skills into agent config directories",
		RunE: func(cmd *cobra.Command, _ []string) error {
			scope, baseDir, err := resolveScope(scopeFlag)
			if err != nil {
				return err
			}
			io := iostream.FromCmd(cmd)
			results := skills.Install(scope, baseDir)
			if jsonOut {
				return iostream.PrintJSON(io.Out, results)
			}
			for _, r := range results {
				if r.Skipped {
					io.Println(ui.Dim(r.Agent + ": skipped (not installed)"))
					continue
				}
				if len(r.Installed) == 0 && len(r.Updated) == 0 {
					io.Println(r.Agent + ": " + ui.Green("all skills up to date"))
					continue
				}
				for _, name := range r.Installed {
					io.Println(r.Agent + ": " + ui.Green("installed "+name))
				}
				for _, name := range r.Updated {
					io.Println(r.Agent + ": " + ui.Yellow("updated "+name))
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&scopeFlag, "scope", "user", "Installation scope: user or project")

	return cmd
}

func newSkillListCmd() *cobra.Command {
	var jsonOut bool
	var scopeFlag string

	cmd := &cobra.Command{
		Use:   cmdList,
		Short: "List bundled skills and their per-agent installation status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			scope, baseDir, err := resolveScope(scopeFlag)
			if err != nil {
				return err
			}
			io := iostream.FromCmd(cmd)
			statuses := skills.Status(scope, baseDir)

			if jsonOut {
				return iostream.PrintJSON(io.Out, statuses)
			}

			skillDefs := skills.All
			io.Printf("\nBundled skills (%d):\n\n", len(skillDefs))

			for i, s := range skillDefs {
				io.Printf("  %s\n", ui.Green(s.Name))
				io.Printf("    %s\n", ui.Dim(s.Description))

				for _, agent := range statuses {
					skill := agent.Skills[i]
					if !agent.Available {
						io.Printf("      %s: %s\n", ui.Dim(agent.Agent), ui.Dim("n/a (agent not installed)"))
						continue
					}
					icon, label := stateDisplay(skill.State)
					io.Printf("      %s: %s %s\n", agent.Agent, icon, label)
				}
				io.Println()
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&scopeFlag, "scope", "user", "Installation scope: user or project")

	return cmd
}

// resolveScope translates the --scope flag value into a Scope and the appropriate base directory.
func resolveScope(scopeFlag string) (skills.Scope, string, error) {
	switch skills.Scope(scopeFlag) {
	case skills.ScopeUser:
		home := os.Getenv(config.EnvHome)
		if home == "" {
			return "", "", &userError{msg: msgHomeNotSet, errMsg: errMsgHomeNotSet}
		}
		return skills.ScopeUser, home, nil
	case skills.ScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", "", fmt.Errorf("get working directory: %w", err)
		}
		return skills.ScopeProject, cwd, nil
	default:
		return "", "", &userError{msg: "invalid scope: must be 'user' or 'project'", errMsg: "invalid scope"}
	}
}

func stateDisplay(state skills.State) (icon, label string) {
	switch state {
	case skills.StateCurrent:
		return ui.Green("✓"), ui.Green("current")
	case skills.StateOutdated:
		return ui.Yellow("⚠"), ui.Yellow("outdated")
	case skills.StateMissing:
		return ui.Dim("✗"), ui.Dim("missing")
	}
	return ui.Dim("?"), ui.Dim("unknown")
}
