package cmd

import "github.com/spf13/cobra"

func newSandboxesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandboxes",
		Short: "Manage sandboxes",
	}
	return cmd
}
