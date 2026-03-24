package cmd

import "github.com/spf13/cobra"

func newUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade chunk to the latest version",
	}
}
