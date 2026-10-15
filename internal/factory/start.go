package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
)

// ErrDiffAlreadyApplied is returned by Start when the diff is already in the
// developer's files, as a git diff of the same checkout is: the worktree starts
// from those files, so there is no first pass left to lay on them.
var ErrDiffAlreadyApplied = errors.New("the diff is already in your files")

// Handoff is the work a developer gives the factory to refine: the prompt that
// says what the change is for, and the work so far as a diff against their
// files. Where the diff was made, by hand, by a local agent or on a sidecar,
// is up to them. With no diff, the work is whatever they go on to do in the
// run's worktree.
type Handoff struct {
	Prompt string
	Diff   []byte
}

// Start makes a run's worktree and branch for work handed to the factory, and
// records it so 'chunk factory build --continue' can refine it. Nothing runs
// on a sidecar. The worktree starts from the developer's files as they are, as
// a build's does, with h's diff applied on top as the work so far.
func Start(ctx context.Context, root string, h Handoff) (Record, error) {
	dataDir, err := runsDir(root)
	if err != nil {
		return Record{}, err
	}
	runID := newRunID()
	wt, err := createRunWorktree(ctx, root, dataDir, runID, h.Prompt)
	if err != nil {
		return Record{}, err
	}
	// How the work is described is decided here, where it is known; the run
	// that refines it reads it from the record.
	rec := newRecord(runID, h.Prompt, wt)
	rec.Brief, rec.NeedsWork = handoffBrief(h.Prompt), true
	err = applyDiff(ctx, wt, h.Diff)
	if err == nil {
		err = saveRecord(dataDir, rec)
	}
	if err != nil {
		// A worktree no record points at could not be refined, so it goes,
		// even when the start was interrupted.
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if rmErr := wt.Remove(rmCtx, root); rmErr != nil {
			return Record{}, errors.Join(err, fmt.Errorf("remove the worktree %s: %w", wt.Path, rmErr))
		}
		return Record{}, err
	}
	return rec, nil
}

// applyDiff lays diff on the worktree's files as uncommitted changes, which
// is where a continued run looks for the work.
func applyDiff(ctx context.Context, wt Worktree, diff []byte) error {
	if len(diff) == 0 {
		return nil
	}
	f, err := os.CreateTemp("", "chunk-factory-*.diff")
	if err != nil {
		return fmt.Errorf("save the diff: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(diff)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("save the diff: %w", err)
	}
	git := gitexec.Runner{Dir: wt.Path}
	if out, err := git.CombinedOutput(ctx, "apply", "--whitespace=nowarn", f.Name()); err != nil {
		// The worktree starts from the developer's uncommitted changes, so a
		// diff of this checkout's own changes is already in it.
		if _, revErr := git.CombinedOutput(ctx, "apply", "--reverse", "--check", f.Name()); revErr == nil {
			return ErrDiffAlreadyApplied
		}
		return fmt.Errorf("apply the diff to your files: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// handoffBrief is how a continued run's implementer is told about work handed
// to the factory: the developer's first pass to finish, not an earlier
// session's to fix.
func handoffBrief(prompt string) string {
	return "You are finishing a change the developer started. They want:\n\n" +
		strings.TrimSpace(prompt) +
		"\n\nThe work so far is in the working tree as uncommitted changes; `git diff HEAD` shows it. It may be a rough first pass: build on it, rather than starting over, until it is ready to merge."
}
