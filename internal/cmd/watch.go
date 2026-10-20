package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
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
		Short:        "Follow and manage your factory runs live",
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

			// A draft made from the dashboard is in the project it was
			// opened in, where `chunk factory build` would work.
			startDir := ""
			if top := gitutil.TopLevelCtx(cmd.Context(), cwd); top != "" {
				startDir = config.CanonicalProjectRoot(top)
			}
			drafts := newWatchDrafts(cmd.Context())
			// Claude runs as a process of its own, which would outlive the
			// dashboard; it is stopped, and waited for, as the dashboard ends.
			defer drafts.stop()
			m := watch.New(entries, !focus).WithRelaunch().WithBottomMargin(editorRowMargin()).
				WithDrafts(drafts.ctx, startDir, drafts.draft, sendDraft)
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
	m := watch.New(remoteProjects(args), len(args) == 0).WithBottomMargin(editorRowMargin())
	p := tea.NewProgram(m, tea.WithContext(cmd.Context()))
	_, err := p.Run()
	return err
}

// editorRowMargin is how many of the terminal's last rows the dashboard leaves
// blank. The built-in terminals of VS Code (and the editors built on it, such
// as Cursor and Windsurf, which name themselves the same way) and of JetBrains
// IDEs can report a couple of rows more than their panel shows, which hid the
// footer below the panel's edge.
func editorRowMargin() int {
	if os.Getenv("TERM_PROGRAM") == "vscode" || os.Getenv("TERMINAL_EMULATOR") == "JetBrains-JediTerm" {
		return 2
	}
	return 0
}

// watchDrafts makes the drafts the dashboard asks Claude for, and stops any
// still being made when the dashboard ends.
type watchDrafts struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newWatchDrafts(parent context.Context) *watchDrafts {
	ctx, cancel := context.WithCancel(parent)
	return &watchDrafts{ctx: ctx, cancel: cancel}
}

func (d *watchDrafts) stop() {
	d.cancel()
	d.wg.Wait()
}

// draft has Claude Code draft prompt on this machine, in a factory run's
// worktree of root, as `chunk factory start` with the draft made by Claude
// would. What a build checks first is checked before Claude starts, so no one
// waits on a draft the factory would refuse. Errors come back as one sentence
// for the dashboard to show.
func (d *watchDrafts) draft(ctx context.Context, root, prompt string, activity func(string)) (watch.Draft, error) {
	d.wg.Add(1)
	defer d.wg.Done()
	if err := checkFactoryProject(ctx, root); err != nil {
		return watch.Draft{}, err
	}
	if err := factory.CheckClaude(); err != nil {
		return watch.Draft{}, errors.New("this machine has no Claude Code to make the draft with; install it from https://claude.com/claude-code")
	}
	rec, err := factory.Start(ctx, root, factory.Handoff{Prompt: prompt})
	if err != nil {
		// Stopped while the worktree was being made, git's error would
		// hide that it was stopped.
		if ctx.Err() != nil {
			return watch.Draft{}, ctx.Err()
		}
		return watch.Draft{}, fmt.Errorf("start a factory run: %w", err)
	}
	// The factory's sidecars take the better part of a minute to start. They
	// start from the run's baseline, which is fixed now, so the daemon makes
	// them while Claude drafts and the developer reads the draft; the run
	// that finishes it takes them ready. A daemon that cannot just leaves the
	// run to make its own.
	// A cancel below waits for this, or it could reach the daemon first and
	// find nothing to cancel.
	warmed := make(chan struct{})
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer close(warmed)
		_ = chunkd.WarmFactory(chunkd.WarmRequest{ProjectRoot: root, RunID: rec.RunID})
	}()
	draft := watch.Draft{RunID: rec.RunID}
	// Claude names files by their path in the run's worktree, which is long
	// and says nothing a path in the repo does not. Claude may spell the
	// worktree with its symlinks resolved.
	sep := string(filepath.Separator)
	pairs := []string{rec.Worktree + sep, ""}
	if resolved, err := filepath.EvalSymlinks(rec.Worktree); err == nil && resolved != rec.Worktree {
		pairs = append(pairs, resolved+sep, "")
	}
	inRepo := strings.NewReplacer(pairs...)
	turn, err := factory.FirstPass(ctx, rec, draftModel, func(a claudecode.Activity) {
		a.Detail = inRepo.Replace(a.Detail)
		activity(activityLine(a))
	})
	draft.Summary = turn.Summary
	// What Claude did before it failed or was stopped is still a draft to
	// look at, so the diff is read either way, on a context of its own.
	diffCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	stat, statErr := factory.WorkStat(diffCtx, rec)
	diff, diffErr := factory.WorkDiff(diffCtx, rec)
	draft.Stat, draft.Diff = stat, diff
	err = errors.Join(err, statErr, diffErr)
	// A draft with nothing in it, or none at all, will not be sent, so its
	// sidecars go now rather than when the daemon gives up waiting.
	if stat == "" || err != nil && !errors.Is(err, context.Canceled) {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			<-warmed
			_ = chunkd.CancelWarm(rec.RunID)
		}()
	}
	return draft, err
}

// draftModel is the model a change started from the dashboard is made with:
// Claude's draft on this machine, and the factory's implementer and reviewers
// that finish it. Sonnet is quicker and cheaper than Claude Code's default
// and strong at code, and the factory's checks still decide when the change
// is done. It is Claude Code's alias, so it follows the latest Sonnet.
const draftModel = "sonnet"

// draftGuidance is what a draft is sent to the factory with. Guidance makes
// the implementer start on the draft at once: without it the factory reviews
// the draft first, a whole round (minutes) spent confirming that a fresh draft
// is unfinished. The reviewers and the commit message see it too, so it reads
// as part of the request.
const draftGuidance = "Finish the draft in the working tree into a change ready to merge."

// sendDraft hands a draft's run to the factory to finish, as `chunk factory
// build --continue <run> --model sonnet "<draftGuidance>"` would.
func sendDraft(root, runID string) (string, error) {
	id, err := chunkd.StartFactory(chunkd.FactoryRequest{ProjectRoot: root, Continue: runID, Prompt: draftGuidance, Model: draftModel})
	var refused *chunkd.SessionRefused
	switch {
	case errors.As(err, &refused):
		return "", errors.New(refused.Message)
	case errors.Is(err, chunkd.ErrDaemonUnavailable):
		return "", errors.New("the chunk daemon is not reachable")
	case err != nil:
		return "", fmt.Errorf("talk to the chunk daemon: %w", err)
	}
	return id, nil
}

// checkFactoryProject checks what `chunk factory build` checks before it
// starts a run in root, as one sentence for the dashboard.
func checkFactoryProject(ctx context.Context, root string) error {
	if _, err := gitremote.URL(ctx, root, "origin"); err != nil {
		return errors.New("this project has no git remote named origin; add one with git remote add origin <url>")
	}
	cfg, err := config.LoadProjectConfig(root)
	if err != nil {
		return errors.New(dashboardMessage(&userError{msg: msgValidateNotConfigured, suggestion: suggestionRunInit, err: err}))
	}
	if _, _, err := factoryChecks(root, "", cfg, false); err != nil {
		return errors.New(dashboardMessage(err))
	}
	return nil
}

// dashboardMessage is err as the dashboard shows it: a user error's message
// and suggestion on one line, or the error itself.
func dashboardMessage(err error) string {
	ue, ok := errors.AsType[*userError](err)
	if !ok {
		return err.Error()
	}
	msg := strings.TrimSuffix(ue.UserMessage(), ".")
	if s := ue.Suggestion(); s != "" {
		msg += ". " + s
	}
	return msg
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
