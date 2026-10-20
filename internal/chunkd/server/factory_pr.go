package server

import (
	"context"
	"errors"
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
	// reviewsDir is the review prompts' directory relative to the project root,
	// "" for the default.
	reviewsDir string
}

// errNoGitHubToken is why a project cannot open a pull request when the daemon
// was started without a token. It is the daemon's own setup rather than
// anything wrong with the request, so a run that insisted on a pull request is
// refused as unavailable and not as a bad request.
var errNoGitHubToken = fmt.Errorf("the chunk daemon has no GitHub token: set %s or run 'chunk auth set github', then restart it", config.EnvGitHubToken)

// planPullRequest checks what opening a pull request needs, before a run that
// would take minutes to reach it starts: a GitHub token, and an origin on
// GitHub to open it on. Its error says what is missing in terms that read both
// as why a run opened no pull request and as why one that had to was refused.
func (d *daemon) planPullRequest(ctx context.Context, root string, draft bool, reviewsDir string) (*pullRequestPlan, error) {
	if d.gh == nil {
		return nil, errNoGitHubToken
	}
	owner, repo, err := gitremote.DetectOrgAndRepoCtx(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("this project's origin remote must be on GitHub (%w)", err)
	}
	return &pullRequestPlan{owner: owner, repo: repo, draft: draft, localBranch: currentBranch(root), reviewsDir: reviewsDir}, nil
}

// refusePullRequest turns a plan that could not be made into the refusal a run
// that insisted on a pull request gets.
func refusePullRequest(err error) error {
	status := http.StatusBadRequest
	if errors.Is(err, errNoGitHubToken) {
		status = http.StatusServiceUnavailable
	}
	return apiErr(status, "cannot open a pull request: %v", err)
}

// factoryPullRequest settles what a starting run will do with its branch and
// starts its record off. A run opens a pull request unless it was told not to,
// so a project that cannot open one is only refused when the request insisted:
// otherwise the run goes ahead and its record says why it opened none.
func (d *daemon) factoryPullRequest(req chunkd.FactoryRequest, root string, record *chunkd.FactoryRun) (*pullRequestPlan, error) {
	switch req.PullRequest {
	case chunkd.PullRequestAuto, chunkd.PullRequestRequired:
	case chunkd.PullRequestNone:
		if req.Draft {
			return nil, apiErr(http.StatusBadRequest, "draft only applies to a run that opens a pull request")
		}
		return nil, nil
	default:
		return nil, apiErr(http.StatusBadRequest, "unknown pull_request %q, want one of %q, %q or %q",
			req.PullRequest, chunkd.PullRequestAuto, chunkd.PullRequestRequired, chunkd.PullRequestNone)
	}
	// Checked now, not after a run that takes minutes: the push and the pull
	// request come last, and need things the run does not.
	plan, err := d.planPullRequest(context.Background(), root, req.Draft, req.ReviewsDir)
	switch {
	case err == nil:
		record.PullRequest = &chunkd.PullRequest{State: chunkd.PRPending, Draft: req.Draft}
		return plan, nil
	case req.PullRequest == chunkd.PullRequestRequired:
		return nil, refusePullRequest(err)
	}
	record.PullRequest = &chunkd.PullRequest{State: chunkd.PRSkipped, Message: err.Error()}
	return nil, nil
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
	// The prompts are linked at the commit the pull request shows, so a link is
	// to what ran.
	src := factory.PromptSource{Prompts: opts.Prompts, Owner: plan.owner, Repo: plan.repo, Dir: plan.reviewsDir}
	if tip, err := rep.Worktree.Tip(openCtx); err == nil {
		src.Commit = tip
	}
	created, err := d.gh.CreatePullRequest(openCtx, plan.owner, plan.repo, github.NewPullRequest{
		Base:  base,
		Head:  branch,
		Title: factory.PullRequestTitle(request, rep.RunID),
		Body:  factory.PullRequestBody(request, rep, src),
		Draft: plan.draft,
	})
	if err != nil {
		// A continued run pushes the same branch a second time, so GitHub
		// refuses a pull request that is already open for it. The push has
		// just updated that one, which is the pull request this run's work is
		// in, so it is the answer rather than the failure.
		if open, lookupErr := d.gh.OpenPullRequestForBranch(openCtx, plan.owner, plan.repo, branch); lookupErr == nil && open != nil {
			entry.setPullRequest(func(pr *chunkd.PullRequest) {
				pr.State, pr.URL, pr.Number = chunkd.PROpened, open.URL, open.Number
			})
			opts.Status(iostream.LevelDone, "Updated pull request "+open.URL)
			return
		}
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
