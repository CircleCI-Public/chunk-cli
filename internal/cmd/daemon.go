package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/daemon"
)

func newDaemonCmd() *cobra.Command {
	var port string

	cmd := &cobra.Command{
		Use:          "daemon",
		Short:        "Start the chunk validation daemon (used in sidecars)",
		SilenceUsage: true,
		Hidden:       true,
		RunE: func(_ *cobra.Command, _ []string) error {
			srv := daemon.NewServer()
			addr := "127.0.0.1:" + port
			fmt.Printf("chunk daemon listening on %s\n", addr)
			return srv.Run(addr)
		},
	}
	cmd.Flags().StringVar(&port, "port", "7777", "TCP port to listen on")
	return cmd
}
