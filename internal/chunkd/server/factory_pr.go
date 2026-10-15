package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// How long each step of opening a pull request may take. A push that hangs on
// a dead connection would otherwise hold the session open until it is cancelled.
const (
	prPushTimeout = 5 * time.Minute
	prOpenTimeout = 2 * time.Minute
)

// pullRequestPlan is what a factory session was asked to do with its branch
// once every check passed.
type pullRequestPlan struct {
	owner, repo string
	draft       bool
	// localBranch is the branch the developer had checked out when the run
	// started, which the pull request targets if the remote has it too.
	localBranch string
}

// planPullRequest checks what opening a pull request needs, before a run that
// would take minutes to reach it starts: a GitHub token, and an origin on
// GitHub to open it on.
func (d *daemon) planPullRequest(ctx context.Context, root string, draft bool) (*pullRequestPlan, error) {
	if d.gh == nil {
		return nil, apiErr(http.StatusServiceUnavailable,
			"opening a pull request needs a GitHub token: set %s or run 'chunk auth set github', then restart the chunk daemon",
			config.EnvGitHubToken)
	}
	owner, repo, err := gitremote.DetectOrgAndRepoCtx(ctx, root)
	if err != nil {
		return nil, apiErr(http.StatusBadRequest, "cannot open a pull request: this project's origin remote must be on GitHub (%v)", err)
	}
	return &pullRequestPlan{owner: owner, repo: repo, draft: draft, localBranch: currentBranch(root)}, nil
}

// setPullRequest updates the session's pull request record under its lock.
func (e *sessionEntry) setPullRequest(update func(*chunkd.PullRequest)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if pr := e.s.Factory.PullRequest; pr != nil {
		update(pr)
	}
}

// openPullRequest pushes a finished run's branch and opens the pull request it
// was asked for, if its checks passed. It records how far it got, and leaves
// the work as it was whatever happens: the branch stays committed locally, and
// stays pushed if the push got that far.
func (d *daemon) openPullRequest(ctx context.Context, entry *sessionEntry, opts factory.RunOptions, rep factory.Report, runErr error) {
	plan := entry.pr
	skip := func(why string) {
		entry.setPullRequest(func(pr *chunkd.PullRequest) { pr.State, pr.Message = chunkd.PRSkipped, why })
	}
	fail := func(err error) {
		opts.Status(iostream.LevelWarn, "could not open the pull request: "+err.Error())
		entry.setPullRequest(func(pr *chunkd.PullRequest) { pr.State, pr.Message = chunkd.PRFailed, err.Error() })
	}
	switch {
	case ctx.Err() != nil:
		skip("the run was cancelled")
		return
	case runErr != nil || !rep.Started:
		skip("the run did not finish")
		return
	case rep.Outcome.Result != factory.ResultPassed:
		skip(factoryResultNote(rep.Outcome))
		return
	case !rep.Committed:
		skip("the work was not committed")
		return
	}

	branch := rep.Worktree.Branch
	entry.setPullRequest(func(pr *chunkd.PullRequest) { pr.State = chunkd.PRPushing })
	opts.Status(iostream.LevelStep, "Pushing "+branch+" to origin...")
	pushCtx, cancel := context.WithTimeout(ctx, prPushTimeout)
	err := rep.Worktree.Push(pushCtx, "origin")
	cancel()
	if err != nil {
		fail(err)
		return
	}

	openCtx, cancel := context.WithTimeout(ctx, prOpenTimeout)
	defer cancel()
	base, err := d.pullRequestBase(openCtx, opts.Root, plan)
	if err != nil {
		fail(err)
		return
	}
	entry.setPullRequest(func(pr *chunkd.PullRequest) { pr.State, pr.Base = chunkd.PROpening, base })
	opts.Status(iostream.LevelStep, fmt.Sprintf("Opening a pull request into %s on %s/%s...", base, plan.owner, plan.repo))

	entry.mu.Lock()
	request := entry.s.Factory.Prompt
	entry.mu.Unlock()
	created, err := d.gh.CreatePullRequest(openCtx, plan.owner, plan.repo, github.NewPullRequest{
		Base:  base,
		Head:  branch,
		Title: factory.PullRequestTitle(request, rep.RunID),
		Body:  factory.PullRequestBody(request, rep.RunID, rep),
		Draft: plan.draft,
	})
	if err != nil {
		fail(err)
		return
	}
	entry.setPullRequest(func(pr *chunkd.PullRequest) {
		pr.State, pr.URL, pr.Number = chunkd.PROpened, created.URL, created.Number
	})
	opts.Status(iostream.LevelDone, "Opened pull request "+created.URL)
}

// pullRequestBase is the branch the pull request targets: the one the
// developer started the run from, if the remote has it, and otherwise the
// repository's default. A branch only they have, or a factory branch when they
// were working in a run's worktree, is not one a pull request can target.
func (d *daemon) pullRequestBase(ctx context.Context, root string, plan *pullRequestPlan) (string, error) {
	local := plan.localBranch
	if local != "" && !strings.HasPrefix(local, "chunk/factory/") {
		if has, err := factory.RemoteHasBranch(ctx, root, "origin", local); err == nil && has {
			return local, nil
		}
	}
	return d.gh.DefaultBranch(ctx, plan.owner, plan.repo)
}
