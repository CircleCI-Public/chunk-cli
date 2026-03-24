package cmd

import "github.com/spf13/cobra"

func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Hook automation for AI coding agents",
	}
	return cmd
}
