package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
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
			if !term.IsTerminal(int(os.Stdout.Fd())) {
				return fmt.Errorf("watch requires a TTY")
			}

			cwd, _ := os.Getwd()
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
				if gitRoot := gitTopLevel(abs); gitRoot != "" {
					abs = gitRoot
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

			m := watch.New(entries)
			p := tea.NewProgram(m)
			_, err := p.Run()
			return err
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Watch all known projects, not just the current directory")
	return cmd
}

// gitTopLevel returns the git repository root for dir, or "" if not in a git repo.
func gitTopLevel(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
