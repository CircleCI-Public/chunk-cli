package cmd

import (
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// newWatchDaemonCmd returns the hidden _daemon subcommand invoked by EnsureRunning
// to start the background watch daemon. It is intentionally hidden from help output.
func newWatchDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    watchDaemonSubcmd,
		Short:  "Run the watch daemon (internal use only)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return watchd.RunDaemon(cmd.Context())
		},
	}
}
