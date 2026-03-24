package cmd

import "github.com/spf13/cobra"

func newValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Run validation commands",
	}
	return cmd
}
