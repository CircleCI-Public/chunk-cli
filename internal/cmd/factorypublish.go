package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/smeltdev/smelt/internal/factory"
	"github.com/smeltdev/smelt/internal/gitremote"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/smeltd"
	"github.com/smeltdev/smelt/internal/ui"
)

// publishFlags is what --push, --pr and the flags that shape the PR asked for.
type publishFlags struct {
	push, pr, none    bool
	base, title, body string
	draft             bool
	bodySet           bool
}

// mode is how the run is published, or an error for flags that contradict.
func (p publishFlags) mode() (factory.PublishMode, error) {
	switch {
	case p.none && (p.push || p.pr):
		return "", newUserError("--no-publish cannot be combined with --push or --pr.").
			withCode("command.invalid_flags").
			withExitCode(ExitBadArgs).
			withoutDetail()
	case p.none:
		return factory.PublishOff, nil
	case p.pr:
		return factory.PublishPR, nil
	case p.push:
		return factory.PublishPush, nil
	}
	return factory.PublishPrompt, nil
}

// canPrompt reports whether there is a person to ask: a terminal on stdin and
// stdout, outside CI, and no program reading --json output.
func canPrompt(in io.Reader, jsonOut bool) bool {
	f, ok := in.(*os.File)
	return ok && !nonInteractive() && !jsonOut && term.IsTerminal(int(f.Fd())) && ui.RequireStdoutTTY() == nil
}

// confirmPublish asks a yes/no question. Swapped out in tests.
var confirmPublish = ui.Confirm

// followAndPublish follows the run to its end, then publishes its branch. The
// run's own error wins over a publish error, since it says how the run ended.
func followAndPublish(ctx context.Context, streams iostream.Streams, id string, jsonOut bool, flags publishFlags, prompt bool) error {
	runErr := followSession(ctx, streams, id, jsonOut)
	if err := publishAfterRun(ctx, streams, id, flags, prompt); err != nil && runErr == nil {
		return err
	}
	return runErr
}

// publishAfterRun offers to push the finished run's branch and open a PR, or
// just pushes when flags say so. The branch is published from the host's
// worktree with the host's git and gh credentials, wherever the run's
// sidecars were. Anything short of a finished run with committed work is left
// alone.
func publishAfterRun(ctx context.Context, streams iostream.Streams, sessionID string, flags publishFlags, prompt bool) error {
	if _, err := flags.mode(); err != nil {
		return err
	}
	detail, err := smeltd.FetchSession(sessionID)
	if err != nil || !detail.State.Finished() || detail.Factory == nil {
		return nil
	}
	return publishRun(ctx, streams, detail.Factory, flags, prompt, hostFor(detail.Factory.Worktree))
}

// publishRun publishes the finished run f through host. Anything short of
// committed work in a kept worktree is left alone.
func publishRun(ctx context.Context, streams iostream.Streams, f *smeltd.FactoryRun, flags publishFlags, prompt bool, host factory.PRHost) error {
	mode, err := flags.mode()
	if err != nil {
		return err
	}
	if f.Worktree == "" || f.WorktreeRemoved || f.Branch == "" {
		return nil
	}

	pub := factory.Publication{
		Dir: f.Worktree, Remote: factory.DefaultRemote, Head: f.Branch,
		BaseBranch: f.BaseBranch, DefaultBranch: f.DefaultBranch, Base: flags.base,
		Title: flags.title, Body: flags.body, Draft: flags.draft,
		Host: host,
	}
	if pub.Title == "" {
		pub.Title = factory.PRTitle(f.Prompt)
	}
	if !flags.bodySet {
		pub.Body = factory.PRBody(f.Prompt, f.RunID, f.Result, f.Rounds, f.Stat)
	}

	_, remoteErr := gitremote.URL(ctx, f.Worktree, pub.Remote)
	facts := factory.PublishFacts{
		Mode: mode, Committed: f.Committed, Passed: f.Result == smeltd.ResultPassed,
		HasRemote: remoteErr == nil, Interactive: prompt,
	}
	// Nothing to do needs neither the guard nor the PR lookup.
	if d := factory.DecidePublish(facts); d.Action == factory.ActionNone {
		explainNoPublish(streams, pub, d)
		return nil
	}
	target, err := pub.Resolve(ctx)
	if err != nil {
		return &userError{msg: "Not publishing " + f.Branch + ".", err: err}
	}
	if target.LookupErr != nil {
		streams.ErrPrintf("Could not check for an existing PR: %v\n", target.LookupErr)
	}
	facts.HasPR = target.Existing != nil
	d := factory.DecidePublish(facts)
	if d.Ask {
		ok, err := confirmPublish(publishQuestion(d.Action, f.Branch, target), true)
		switch {
		case errors.Is(err, ui.ErrCancelled), err == nil && !ok:
			streams.ErrPrintf("Not publishing. Publish it later with: git push -u %s %s\n", pub.Remote, f.Branch)
			return nil
		case err != nil:
			return fmt.Errorf("ask whether to publish: %w", err)
		}
	}
	return runPublish(ctx, streams, pub, target, d.Action)
}

// explainNoPublish says why a run's branch was left local, and how to push it.
func explainNoPublish(streams iostream.Streams, pub factory.Publication, d factory.PublishDecision) {
	if d.Reason == "" {
		return
	}
	streams.ErrPrintf("Not publishing %s: %s.\n", pub.Head, d.Reason)
	streams.ErrPrintf("Publish it later with: git -C %s push -u %s %s\n", pub.Dir, pub.Remote, pub.Head)
}

// hostFor is the PR host for the worktree, or nil without the GitHub CLI.
func hostFor(dir string) factory.PRHost {
	if h := factory.NewGHHost(dir); h != nil {
		return h
	}
	return nil
}

func publishQuestion(action factory.PublishAction, head string, t factory.PublishTarget) string {
	if action == factory.ActionUpdatePR {
		return fmt.Sprintf("Branch %s already has PR #%d (%s). Push the latest commit to update it?", head, t.Existing.Number, t.Existing.URL)
	}
	return fmt.Sprintf("Push %s and open a PR into %s?", head, t.Base)
}

// runPublish takes the decided action. A failed push is an error; a PR that
// could not be opened is not, since the push stands and the compare URL says
// how to finish by hand.
func runPublish(ctx context.Context, streams iostream.Streams, pub factory.Publication, t factory.PublishTarget, action factory.PublishAction) error {
	status := newStatusFunc(streams)
	if err := pub.Push(ctx); err != nil {
		return &userError{msg: "Could not push " + pub.Head + ".", err: err}
	}
	status(iostream.LevelDone, "Pushed "+pub.Head+" to "+pub.Remote)
	switch action {
	case factory.ActionUpdatePR:
		status(iostream.LevelDone, fmt.Sprintf("Updated PR #%d: %s", t.Existing.Number, t.Existing.URL))
	case factory.ActionOpenPR:
		url, err := pub.OpenPR(ctx, t)
		if err == nil {
			status(iostream.LevelDone, "Opened PR: "+url)
			return nil
		}
		status(iostream.LevelWarn, fmt.Sprintf("could not open a PR: %v", err))
		if u := pub.CompareURL(ctx, t); u != "" {
			streams.ErrPrintf("  Open one at: %s\n", u)
		}
	case factory.ActionPush, factory.ActionNone:
	}
	return nil
}
