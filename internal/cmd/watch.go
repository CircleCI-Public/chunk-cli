package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/tui/watch"
)

func newWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "watch",
		Short:        "Live dashboard for active sidecars and recent activity",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !term.IsTerminal(int(os.Stdout.Fd())) {
				return fmt.Errorf("watch requires a TTY")
			}

			dataDir, err := sidecar.StateDir()
			if err != nil {
				return fmt.Errorf("watch: could not resolve project data dir: %w", err)
			}

			el, err := eventlog.Open(dataDir)
			if err != nil {
				return fmt.Errorf("watch: could not open event log: %w", err)
			}

			cwd, _ := os.Getwd()

			m := watch.New(el, dataDir, cwd)
			p := tea.NewProgram(m)
			_, err = p.Run()
			return err
		},
	}
}
