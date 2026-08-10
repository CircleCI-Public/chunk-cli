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
	var userScope bool
	var projectScope bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install or update all skills into agent config directories",
		RunE: func(cmd *cobra.Command, _ []string) error {
			scope, baseDir, err := resolveScope(userScope, projectScope)
			if err != nil {
				return err
			}
			io := iostream.FromCmd(cmd)
			results := skills.Install(scope, baseDir)
			if jsonOut {
				if err := iostream.PrintJSON(io.Out, results); err != nil {
					return err
				}
				for _, r := range results {
					if len(r.Errors) > 0 {
						return fmt.Errorf("one or more skills failed to install")
					}
				}
				return nil
			}
			var installErr error
			for _, r := range results {
				if r.Skipped {
					io.Println(ui.Dim(r.Agent + ": skipped (not installed)"))
					continue
				}
				for _, msg := range r.Errors {
					io.ErrPrintln(ui.Warning(r.Agent + ": error: " + msg))
					installErr = fmt.Errorf("one or more skills failed to install")
				}
				if len(r.Installed) == 0 && len(r.Updated) == 0 && len(r.Errors) == 0 {
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
			return installErr
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&userScope, "user", false, "Install into user-level agent config directories (~/.claude/skills, ~/.agents/skills)")
	cmd.Flags().BoolVar(&projectScope, "project", false, "Install into project-level agent config directories (.claude/skills, .agents/skills) [default]")

	return cmd
}

func newSkillListCmd() *cobra.Command {
	var jsonOut bool
	var userScope bool
	var projectScope bool

	cmd := &cobra.Command{
		Use:   cmdList,
		Short: "List bundled skills and their per-agent installation status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			scope, baseDir, err := resolveScope(userScope, projectScope)
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
	cmd.Flags().BoolVar(&userScope, "user", false, "List user-level skill installation status (~/.claude/skills, ~/.agents/skills)")
	cmd.Flags().BoolVar(&projectScope, "project", false, "List project-level skill installation status (.claude/skills, .agents/skills) [default]")

	return cmd
}

// resolveScope picks a Scope and base directory from the --user / --project flags.
// Project scope is the default when neither flag is set.
func resolveScope(userFlag, projectFlag bool) (skills.Scope, string, error) {
	if userFlag && projectFlag {
		return "", "", &userError{msg: "--user and --project are mutually exclusive", errMsg: "mutually exclusive flags"}
	}
	if userFlag {
		home := os.Getenv(config.EnvHome)
		if home == "" {
			return "", "", &userError{msg: msgHomeNotSet, errMsg: errMsgHomeNotSet}
		}
		return skills.ScopeUser, home, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("get working directory: %w", err)
	}
	return skills.ScopeProject, cwd, nil
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
