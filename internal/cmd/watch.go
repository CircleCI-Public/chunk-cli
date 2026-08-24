package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	internaltui "github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/tui/watch"
)

func newWatchCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:          "watch [dir...]",
		Short:        "Live dashboard for active sidecars and recent activity",
		SilenceUsage: true,
		Args:         cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := internaltui.RequireStdoutTTY(); err != nil {
				return fmt.Errorf("watch requires a TTY")
			}

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("watch: could not determine working directory: %w", err)
			}
			roots := []string{cwd}
			if all {
				known, err := sidecar.AllProjectRoots()
				if err != nil {
					return fmt.Errorf("watch: could not list projects: %w", err)
				}
				roots = append(roots, known...)
			}
			roots = append(roots, args...)

			seen := map[string]bool{}
			var entries []watch.ProjectEntry
			for _, root := range roots {
				abs, err := filepath.Abs(root)
				if err != nil {
					return fmt.Errorf("watch: invalid path %q: %w", root, err)
				}
				if gitRoot := gitutil.TopLevelCtx(cmd.Context(), abs); gitRoot != "" {
					abs = gitRoot
				} else {
					// Skip non-git paths: nothing to watch and no sidecar to find.
					continue
				}
				if seen[abs] {
					continue
				}
				seen[abs] = true

				dataDir, err := config.ProjectDataDir(abs)
				if err != nil {
					return fmt.Errorf("watch: data dir for %s: %w", abs, err)
				}

				// Register this project so future --all runs find it.
				if err := os.MkdirAll(dataDir, 0o755); err == nil {
					_ = os.WriteFile(filepath.Join(dataDir, "project-root"), []byte(abs), 0o644)
				}

				el, err := eventlog.Open(dataDir)
				if err != nil {
					return fmt.Errorf("watch: event log for %s: %w", abs, err)
				}

				entries = append(entries, watch.ProjectEntry{
					Log:         el,
					DataDir:     dataDir,
					ProjectRoot: abs,
				})
			}

			m := watch.New(entries, all)
			p := tea.NewProgram(m, tea.WithContext(cmd.Context()))
			_, err = p.Run()
			return err
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Watch all known projects, not just the current directory")
	cmd.AddCommand(newWatchDaemonCmd())
	return cmd
}
