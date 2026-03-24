package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/skills"
)

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage skills",
	}

	cmd.AddCommand(newSkillsInstallCmd())
	cmd.AddCommand(newSkillsListCmd())
	return cmd
}

func newSkillsInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install all skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			home := os.Getenv("HOME")
			if home == "" {
				return fmt.Errorf("HOME not set")
			}
			if err := skills.Install(home); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Skills installed successfully.")
			return nil
		},
	}
}

func newSkillsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available skills",
		Run: func(cmd *cobra.Command, args []string) {
			home := os.Getenv("HOME")
			infos := skills.List(home)
			for _, info := range infos {
				status := "not installed"
				if info.Installed {
					status = "installed"
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s (%s)\n", info.Name, status)
			}
		},
	}
}
