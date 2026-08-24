package cmd

import (
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry/receiver"
)

// newReceiveTelemetryCmd builds the hidden receive-telemetry command. chunk
// re-execs itself with this subcommand to deliver buffered telemetry events
// out of process (see internal/telemetry/delegate.go): the parent serializes
// events as JSON to this command's stdin, and this command forwards them to
// Segment. Telemetry is disabled here so receiving telemetry never emits its own.
func newReceiveTelemetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "receive-telemetry",
		Short:        "Receive telemetry events and forward them to Segment",
		Hidden:       true,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return receiver.Receive(cmd.InOrStdin())
		},
	}
	telemetry.DisableTelemetry(cmd)
	return cmd
}
