package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/skills"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Install and manage AI agent skills",
	}

	cmd.AddCommand(newSkillsInstallCmd())
	cmd.AddCommand(newSkillsListCmd())
	return cmd
}

func newSkillsInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install or update all skills into agent config directories",
		RunE: func(cmd *cobra.Command, args []string) error {
			home := os.Getenv("HOME")
			if home == "" {
				return fmt.Errorf("HOME not set")
			}
			io := iostream.FromCmd(cmd)
			results := skills.Install(home)
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
}

func newSkillsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List bundled skills and their per-agent installation status",
		Run: func(cmd *cobra.Command, args []string) {
			home := os.Getenv("HOME")
			io := iostream.FromCmd(cmd)
			statuses := skills.Status(home)

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
		},
	}
}

func stateDisplay(state skills.State) (icon, label string) {
	switch state {
	case skills.StateCurrent:
		return ui.Green("\u2713"), ui.Green("current")
	case skills.StateOutdated:
		return ui.Yellow("\u26a0"), ui.Yellow("outdated")
	default:
		return ui.Dim("\u2717"), ui.Dim("missing")
	}
}
