package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
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
			iostream.FromCmd(cmd).ErrPrintln("Skills installed successfully.")
			return nil
		},
	}
}

func newSkillsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available skills",
		Run: func(cmd *cobra.Command, args []string) {
			io := iostream.FromCmd(cmd)
			home := os.Getenv("HOME")
			infos := skills.List(home)
			for _, info := range infos {
				status := "not installed"
				if info.Installed {
					status = "installed"
				}
				io.Printf("  %s (%s)\n", info.Name, status)
			}
		},
	}
}
