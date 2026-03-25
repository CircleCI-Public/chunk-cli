package cmd

import (
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/upgrade"
)

func newUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade chunk to the latest version",
		RunE: func(_ *cobra.Command, _ []string) error {
			return upgrade.Run()
		},
	}
}
