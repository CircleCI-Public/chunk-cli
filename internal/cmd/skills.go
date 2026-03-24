package cmd

import "github.com/spf13/cobra"

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage skills",
	}
	return cmd
}
