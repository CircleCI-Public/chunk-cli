package cmd

import (
	"github.com/spf13/cobra"
)

func newFactoryCmd() *cobra.Command {
	// Unknown flags are not whitelisted here: a flag meant for a subcommand
	// but written before it would consume the next word as its value, and
	// print help with exit code 0 instead of reporting the mistake.
	cmd := &cobra.Command{
		Use:   "factory",
		Short: "Plan changes and build them in factory",
		RunE:  groupRunE,
	}
	cmd.AddCommand(newFactoryPlanCmd())
	cmd.AddCommand(newFactoryAgentCmd())
	cmd.AddCommand(newFactoryBootstrapCmd())
	return cmd
}
