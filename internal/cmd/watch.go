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
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	internaltui "github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/tui/watch"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// watchCmdName is the name of the watch command. It is referenced by the
// daemon re-exec argv and by the update-check skip list, so it lives here
// next to the command it names.
const watchCmdName = "watch"

// watchDaemonSubcmd is the hidden subcommand name used to spawn the background
// watch daemon process. Defined here so all cmd-package callers share one constant.
const watchDaemonSubcmd = "_daemon"

func newWatchCmd() *cobra.Command {
	var (
		focus bool
		all   bool
	)

	cmd := &cobra.Command{
		Use:          "watch [dir...]",
		Short:        "Live dashboard for active sidecars and recent activity",
		SilenceUsage: true,
		Args:         cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := internaltui.RequireStdoutTTY(); err != nil {
				return fmt.Errorf("watch requires a TTY")
			}

			daemonArgs := []string{watchCmdName, watchDaemonSubcmd}
			if err := watchd.EnsureRunning(daemonArgs); err != nil {
				iostream.FromCmd(cmd).ErrPrintf("chunk watch: daemon unavailable, running without background updates: %v\n", err)
			}

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("watch: could not determine working directory: %w", err)
			}
			roots, err := watchRoots(cwd, focus, args)
			if err != nil {
				return err
			}

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

				// Register this project so future runs discover it.
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

			m := watch.New(entries, !focus).WithDaemonArgs(daemonArgs)
			p := tea.NewProgram(m, tea.WithContext(cmd.Context()))
			_, err = p.Run()
			return err
		},
	}

	cmd.Flags().BoolVar(&focus, "focus", false, "Watch only the current directory instead of all known projects")
	// --all is now the default; keep the flag so existing invocations keep working.
	cmd.Flags().BoolVar(&all, "all", false, "Watch all known projects (default)")
	_ = cmd.Flags().MarkDeprecated("all", "watching all known projects is now the default; use --focus to watch only the current directory")
	cmd.AddCommand(newWatchDaemonCmd())
	return cmd
}

// watchRoots returns the directories the dashboard should watch: the current
// directory plus any explicitly named ones, and — unless focus is set — every
// project chunk has seen before.
func watchRoots(cwd string, focus bool, args []string) ([]string, error) {
	roots := []string{cwd}
	if !focus {
		known, err := sidecar.AllProjectRoots()
		if err != nil {
			return nil, fmt.Errorf("watch: could not list projects: %w", err)
		}
		roots = append(roots, known...)
	}
	return append(roots, args...), nil
}
