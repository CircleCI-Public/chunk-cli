package cmd

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/watch"
)

// watchCmdName is the name of the watch command, referenced by the update-check
// and auto-launch skip lists.
const watchCmdName = "watch"

func newWatchCmd() *cobra.Command {
	var (
		focus bool
		all   bool
	)

	cmd := &cobra.Command{
		Use:          "watch [dir...]",
		Short:        "Live dashboard for active pools and recent activity",
		SilenceUsage: true,
		Args:         cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ui.RequireStdoutTTY(); err != nil {
				return fmt.Errorf("watch requires a TTY")
			}

			if chunkd.CurrentConnection().Remote != "" {
				return runRemoteWatch(cmd, focus, args)
			}

			if err := chunkd.EnsureRunning(); err != nil {
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
					// Canonicalised because --focus sends these roots to the daemon as a
					// filter, and the daemon keys projects by the canonical spelling. Git
					// happens to answer with a resolved path on darwin, so a mismatch here
					// would be invisible on one platform and an empty dashboard on another.
					abs = config.CanonicalProjectRoot(gitRoot)
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
				_ = sidecar.RegisterProjectRoot(dataDir, abs)

				entries = append(entries, watch.ProjectEntry{ProjectRoot: abs})
			}

			m := watch.New(entries, !focus).WithRelaunch()
			p := tea.NewProgram(m, tea.WithContext(cmd.Context()))
			_, err = p.Run()
			return err
		},
	}

	cmd.Flags().BoolVar(&focus, "focus", false, "Watch only the current directory instead of all known projects")
	// --all is now the default; keep the flag so existing invocations keep working.
	cmd.Flags().BoolVar(&all, "all", false, "Watch all known projects (default)")
	_ = cmd.Flags().MarkDeprecated("all", "watching all known projects is now the default; use --focus to watch only the current directory")
	return cmd
}

// runRemoteWatch runs the dashboard against a daemon on another machine
// (CHUNK_DAEMON_REMOTE_ADDR).
//
// Nothing about the local machine is used to pick what to show. The daemon
// tracks its own projects at its own paths, so this machine's working directory,
// git root and project registry describe a different set of repos; sending them
// as a filter would match nothing and leave an empty dashboard that looks like a
// quiet one. The default is therefore everything the daemon tracks. Paths given
// as arguments are taken as paths on the daemon's host and sent as typed.
func runRemoteWatch(cmd *cobra.Command, focus bool, args []string) error {
	if chunkd.TCPToken() == "" {
		// A daemon only listens on TCP with a token, so a client without one
		// cannot be talking to a working daemon. Say so before the dashboard
		// clears the screen and a 401 becomes an unexplained red header.
		return newUserError("CHUNK_DAEMON_REMOTE_ADDR is set but CHUNK_DAEMON_TCP_TOKEN is not.").
			withCode("watch.remote_token_missing").
			withSuggestion("Set CHUNK_DAEMON_TCP_TOKEN to the token the remote daemon was started with.").
			withoutDetail()
	}
	if focus && len(args) == 0 {
		return newUserError("--focus needs at least one path on the remote daemon's host.").
			withCode("command.invalid_args").
			withSuggestion("Run: chunk watch --focus /path/on/the/daemon/host, or drop --focus to watch every project the daemon tracks.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	m := watch.New(remoteProjects(args), len(args) == 0)
	p := tea.NewProgram(m, tea.WithContext(cmd.Context()))
	_, err := p.Run()
	return err
}

// remoteProjects turns paths on the daemon's host into dashboard entries.
// They are cleaned but not resolved: resolving would consult this machine's
// filesystem about a path that lives on another one.
func remoteProjects(paths []string) []watch.ProjectEntry {
	entries := make([]watch.ProjectEntry, 0, len(paths))
	for _, p := range paths {
		entries = append(entries, watch.ProjectEntry{ProjectRoot: path.Clean(p)})
	}
	return entries
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
