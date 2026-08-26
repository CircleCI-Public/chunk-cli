package cmd

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/daemon"
	"github.com/CircleCI-Public/chunk-cli/internal/tui/statusview"
)

func newStatusCmd() *cobra.Command {
	var port, sidecarID, sidecarName string

	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Show live validation status (runs inside sidecar tmux pane)",
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if sidecarID == "" {
				return fmt.Errorf("--sidecar-id is required")
			}
			dc := daemon.NewLocalClient("http://localhost:" + port)
			m := statusview.New(sidecarID, sidecarName, dc)
			p := tea.NewProgram(m, tea.WithContext(cmd.Context()))
			_, err := p.Run()
			return err
		},
	}
	cmd.Flags().StringVar(&port, "port", "7777", "daemon port")
	cmd.Flags().StringVar(&sidecarID, "sidecar-id", "", "sidecar ID to show status for")
	cmd.Flags().StringVar(&sidecarName, "sidecar-name", "", "sidecar display name")
	return cmd
}
