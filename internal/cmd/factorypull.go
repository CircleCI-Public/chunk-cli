package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newFactoryPullCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "pull [run id]",
		Short: "Bring a remote factory build run's work back to this machine",
		Long: `Brings the work a 'chunk factory build --remote' run did on its own
sidecar back into this run's local worktree.

With no run id, this picks the most recently started remote run in this
project; with one, that run, by its ID or its branch. A run that is not
remote - never started with --remote, or already pulled - is refused.

If the run has not finished yet, this waits it out exactly as --remote does:
the host's stderr reaches the terminal as it streams, and Ctrl-C detaches
without stopping the run or pulling anything. Once the run has finished, its
work is found by reading the host's own run record over SSH, not by parsing
anything it printed, and pulled from the host's worktree into this run's
worktree. Files the run added are then staged with 'git add -A -N', so they
show up in 'git diff' rather than as untracked.

Only once the work is local does this tear the host down: its sidecar is
deleted, the pool's state file removed, and the record's remote run cleared,
leaving an ordinary local run pointing at the pulled worktree. A failure
before the work is local changes nothing, so a pull is always safe to run
again.`,
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)
			statusFn := newStatusFunc(streams)

			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			dataDir, err := config.RepoDataDir(root)
			if err != nil {
				return fmt.Errorf("find chunk's data directory for the repository: %w", err)
			}

			rec, err := factoryPullTarget(root, dataDir, args)
			if err != nil {
				return err
			}

			rc, _ := config.Resolve("", "", insecureStorageFlag(cmd))
			client, err := ensureCircleCIClient(ctx, cmd, rc, streams, ui.PromptHidden)
			if err != nil {
				return err
			}

			if err := pullRemoteRun(ctx, client, root, streams, statusFn, &rec); err != nil {
				if errors.Is(err, errDetached) {
					return nil
				}
				return err
			}
			if err := factory.SaveRecord(dataDir, rec); err != nil {
				statusFn(iostream.LevelWarn, fmt.Sprintf("could not update the run's record: %v", err))
			}

			if jsonOut {
				return iostream.PrintJSON(streams.Out, rec)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// factoryPullTarget resolves the record 'chunk factory pull' should act on:
// the run named by args, or with none, the most recently saved record in
// dataDir that still has a remote run. Either way, a record with no remote
// run is refused: there is nothing for a pull to bring back.
func factoryPullTarget(root, dataDir string, args []string) (factory.Record, error) {
	if len(args) == 0 {
		runID, err := latestRemoteRunID(dataDir)
		if err != nil {
			return factory.Record{}, err
		}
		return factory.LoadRecord(root, runID)
	}

	runID := factory.ParseRunID(args[0])
	rec, err := factory.LoadRecord(root, runID)
	switch {
	case errors.Is(err, factory.ErrNoRecord):
		return factory.Record{}, newUserError(fmt.Sprintf("No factory run %s in this project.", runID)).
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			withoutDetail()
	case err != nil:
		return factory.Record{}, &userError{msg: fmt.Sprintf("Could not read factory run %s.", runID), err: err}
	}
	if rec.Remote == nil {
		return factory.Record{}, noRemoteRunError(runID)
	}
	return rec, nil
}

// noRemoteRunError says there is nothing for 'chunk factory pull' to bring
// back: the named run, or any run in the project, was never started with
// --remote, or was already pulled.
func noRemoteRunError(runID string) error {
	msg := "No remote factory run to pull."
	if runID != "" {
		msg = fmt.Sprintf("Factory run %s is not on a sidecar; there is nothing to pull.", runID)
	}
	return newUserError(msg).
		withSuggestion("Start one with: chunk factory build --remote").
		withCode("command.invalid_args").
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// latestRemoteRunID returns the ID of the most recently saved record in
// dataDir that has a remote run. Recency is each record file's own
// modification time, since a record carries no timestamp of its own, and
// SaveRecord rewrites the file every time a run's state changes.
func latestRemoteRunID(dataDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dataDir, "factory", "*.json"))
	if err != nil {
		return "", fmt.Errorf("list factory records: %w", err)
	}
	var bestID string
	var bestTime time.Time
	for _, m := range matches {
		if strings.HasSuffix(m, ".pool.json") {
			continue
		}
		info, statErr := os.Stat(m)
		if statErr != nil {
			continue
		}
		data, readErr := os.ReadFile(m)
		if readErr != nil {
			continue
		}
		var rec factory.Record
		if err := json.Unmarshal(data, &rec); err != nil || rec.Remote == nil {
			continue
		}
		if bestID == "" || info.ModTime().After(bestTime) {
			bestID, bestTime = rec.RunID, info.ModTime()
		}
	}
	if bestID == "" {
		return "", noRemoteRunError("")
	}
	return bestID, nil
}

// pullRemoteRun brings rec's remote run back into its local worktree and
// tears the host down. On success it clears rec.Remote, so the caller can
// save an ordinary local record. On any error, or when Ctrl-C detaches
// (errDetached), rec is left untouched, so a pull can always be run again.
func pullRemoteRun(
	ctx context.Context, client *circleci.Client, root string,
	streams iostream.Streams, statusFn iostream.StatusFunc, rec *factory.Record,
) error {
	remote := rec.Remote
	pullHint := "chunk factory pull " + rec.RunID

	cmdInfo, err := client.GetCommand(ctx, remote.CommandID)
	if err != nil {
		return &userError{msg: fmt.Sprintf("Could not check the run on sidecar %s.", remote.SidecarID), err: err}
	}
	switch {
	case cmdInfo.EndedAt == nil:
		if err := followRemoteBuild(ctx, client, remote.SidecarID, remote.CommandID, streams, statusFn, pullHint); err != nil {
			return err
		}
	case cmdInfo.ExitCode != nil && *cmdInfo.ExitCode != 0:
		return &userError{
			msg:        fmt.Sprintf("The run failed on sidecar %s.", remote.SidecarID),
			suggestion: fmt.Sprintf("Inspect it with: chunk sidecar ssh --sidecar-id %s", remote.SidecarID),
			err:        fmt.Errorf("exit %d", *cmdInfo.ExitCode),
		}
	}

	statusFn(iostream.LevelStep, "Finding the host's worktree...")
	hostWorktree, err := hostRunWorktree(ctx, client, remote.SidecarID)
	if err != nil {
		return &userError{msg: fmt.Sprintf("Could not read the host's run record on sidecar %s.", remote.SidecarID), err: err}
	}

	statusFn(iostream.LevelStep, "Pulling the work from the host...")
	if err := sidecar.RsyncPull(ctx, client, remote.SidecarID, hostWorktree, rec.Worktree, statusFn); err != nil {
		return &userError{msg: "Could not pull the work from the host.", err: err}
	}
	if out, err := (gitexec.Runner{Dir: rec.Worktree}).CombinedOutput(ctx, "add", "-A", "-N"); err != nil {
		return &userError{msg: "Could not stage the pulled work.", err: fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))}
	}

	statusFn(iostream.LevelDone, fmt.Sprintf("Pulled the work from sidecar %s.", remote.SidecarID))
	streams.ErrPrintf("  Worktree: %s\n  See it with: git -C %s diff\n", rec.Worktree, rec.Worktree)

	// The work is local now: what is left is cleaning up the host, which is
	// best-effort. A hiccup here is only warned about, since rec.Remote is
	// about to be cleared either way and a stray sidecar can be deleted by
	// hand with 'chunk sidecar delete'.
	if err := client.DeleteSidecar(ctx, remote.SidecarID); err != nil {
		statusFn(iostream.LevelWarn, fmt.Sprintf("could not delete sidecar %s: %v", remote.SidecarID, err))
	}
	poolName := "factory-remote-" + rec.RunID
	if statePath, perr := remotePoolStatePath(root, poolName); perr != nil {
		statusFn(iostream.LevelWarn, fmt.Sprintf("could not resolve the pool's state path: %v", perr))
	} else if rmErr := os.Remove(statePath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		statusFn(iostream.LevelWarn, fmt.Sprintf("could not remove the pool state %s: %v", statePath, rmErr))
	}

	rec.Remote = nil
	return nil
}

// hostRunWorktree reads the host's own factory run record over SSH and
// returns the worktree its 'factory build' made, rather than trusting
// anything the run printed: Remote.HostRepo is the synced checkout, which is
// not where the work is, and the run may have printed little or nothing by
// the time this runs.
func hostRunWorktree(ctx context.Context, client *circleci.Client, sidecarID string) (string, error) {
	entry := &sidecar.PoolEntry{ID: sidecarID, Client: client}
	out, err := review.RunScript(ctx, sidecar.ClientExec, entry, factory.HostRecordScript(), factoryRemoteScriptLimit)
	if err != nil {
		return "", err
	}
	var hostRec factory.Record
	if err := json.Unmarshal([]byte(out), &hostRec); err != nil {
		return "", fmt.Errorf("parse the host's run record: %w", err)
	}
	if hostRec.Worktree == "" {
		return "", fmt.Errorf("the host's run record names no worktree")
	}
	return hostRec.Worktree, nil
}
