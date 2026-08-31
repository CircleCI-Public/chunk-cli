package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

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
		Short: "Install the CircleCI plugin into your AI agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			scope, err := resolveScope(userScope, projectScope)
			if err != nil {
				return err
			}
			io := iostream.FromCmd(cmd)
			results := skills.Install(scope)
			if jsonOut {
				if err := iostream.PrintJSON(io.Out, results); err != nil {
					return err
				}
				for _, r := range results {
					if len(r.Errors) > 0 {
						return fmt.Errorf("one or more agents failed to install")
					}
				}
				return nil
			}
			var installErr error
			for _, r := range results {
				if r.Skipped {
					io.Println(ui.Dim(r.Agent + ": skipped (claude CLI not found)"))
					continue
				}
				for _, msg := range r.Errors {
					io.ErrPrintln(ui.Warning(r.Agent + ": error: " + msg))
					installErr = fmt.Errorf("one or more agents failed to install")
				}
				if len(r.Installed) == 0 && len(r.Updated) == 0 && len(r.Errors) == 0 {
					io.Println(r.Agent + ": " + ui.Green("all skills up to date"))
					continue
				}
				for _, name := range r.Installed {
					io.Println(r.Agent + ": " + ui.Green("installed "+name))
				}
				for _, name := range r.Updated {
					io.Println(r.Agent + ": " + ui.Yellow("already installed "+name))
				}
			}
			return installErr
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&userScope, "user", false, "Install at user scope (~/.claude)")
	cmd.Flags().BoolVar(&projectScope, "project", false, "Install at project scope (.claude in the current directory) [default]")

	return cmd
}

func newSkillListCmd() *cobra.Command {
	var jsonOut bool
	var userScope bool
	var projectScope bool

	cmd := &cobra.Command{
		Use:   cmdList,
		Short: "Show CircleCI plugin installation status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			scope, err := resolveScope(userScope, projectScope)
			if err != nil {
				return err
			}
			io := iostream.FromCmd(cmd)
			statuses := skills.Status(scope, "")

			if jsonOut {
				return iostream.PrintJSON(io.Out, statuses)
			}

			skillDefs := skills.All
			io.Printf("\nCircleCI plugin skills (%d):\n\n", len(skillDefs))

			for _, s := range skillDefs {
				io.Printf("  %s\n", ui.Green(s.Name))
				io.Printf("    %s\n", ui.Dim(s.Description))
			}
			io.Println()

			for _, agent := range statuses {
				if !agent.Available {
					io.Printf("  %s: %s\n", ui.Dim(agent.Agent), ui.Dim("n/a (claude CLI not found)"))
					continue
				}
				for _, skill := range agent.Skills {
					icon, label := stateDisplay(skill.State)
					io.Printf("  %s: %s %s\n", agent.Agent, icon, label)
				}
			}
			io.Println()
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&userScope, "user", false, "Show user-scope installation status")
	cmd.Flags().BoolVar(&projectScope, "project", false, "Show project-scope installation status [default]")

	return cmd
}

// resolveScope picks a Scope from the --user / --project flags.
// Project scope is the default when neither flag is set.
func resolveScope(userFlag, projectFlag bool) (skills.Scope, error) {
	if userFlag && projectFlag {
		return "", &userError{msg: "--user and --project are mutually exclusive", errMsg: "mutually exclusive flags"}
	}
	if userFlag {
		return skills.ScopeUser, nil
	}
	return skills.ScopeProject, nil
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
