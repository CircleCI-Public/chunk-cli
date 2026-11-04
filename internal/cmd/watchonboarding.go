package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/browser"
	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/onboarding"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/watch"
)

// onboardingDeps wires onboarding to the daemon's Setup run, CircleCI, the
// dashboard's plans, GitHub and the browser, in root's project.
func onboardingDeps(root string, plans *watchPlans, store onboarding.StateStore) onboarding.Deps {
	return onboarding.Deps{
		Root:    root,
		Setup:   daemonSetup{root: root},
		Account: circleCIAccount{root: root},
		Open:    onboardingOpener(plans.plan),
		Checks:  githubChecks(),
		Browser: browser.Open,
		State:   store,
	}
}

// onboardingOpener is open as onboarding opens a plan, with the opened
// plan's Root kept as it is.
func onboardingOpener(open watch.PlanOpener) onboarding.Opener {
	return func(ctx context.Context, root, id string) (onboarding.Opened, error) {
		opened, err := open(ctx, root, id)
		return onboarding.Opened{
			Agent:   opened.Agent,
			Approve: opened.Approve,
			Store:   opened.Store,
			Root:    opened.Root,
		}, err
	}
}

// daemonSetup is the project's Setup run in the chunk daemon.
type daemonSetup struct{ root string }

func (d daemonSetup) Start(_ context.Context, orgID string) (chunkd.SetupActivity, error) {
	return chunkd.StartSetup(chunkd.SetupRequest{ProjectRoot: d.root, OrgID: orgID})
}

func (d daemonSetup) Fetch(_ context.Context, after int) (chunkd.SetupActivity, bool, error) {
	return chunkd.FetchSetup(d.root, after)
}

func (d daemonSetup) Continue(context.Context) error { return chunkd.ContinueSetup(d.root) }

// circleCIAccount is the developer's CircleCI sign-in and the project's org:
// the configured one, else their collaborations.
type circleCIAccount struct{ root string }

func (a circleCIAccount) Org(ctx context.Context) (string, []onboarding.Org, error) {
	client, err := onboardingClient()
	if err != nil {
		return "", nil, err
	}
	if org, _ := config.ResolveOrgID(a.root); org != "" {
		return org, nil, nil
	}
	collabs, err := client.ListCollaborations(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("list your CircleCI organizations: %w", err)
	}
	orgs := make([]onboarding.Org, 0, len(collabs))
	for _, c := range collabs {
		orgs = append(orgs, onboarding.Org{ID: c.ID, Name: c.Name})
	}
	return "", orgs, nil
}

// onboardingClient is factoryPlanClient's client, with no token reported as
// onboarding.ErrSignedOut for the sign-in card.
func onboardingClient() (*circleci.Client, error) {
	rc, err := config.ResolveCircleCI(false)
	if err != nil {
		return nil, fmt.Errorf("load the CircleCI config: %w", err)
	}
	client, err := authprompt.ResolveCircleCIClient(rc, nil)
	if errors.Is(err, authprompt.ErrNeedsAuth) {
		return nil, onboarding.ErrSignedOut
	}
	if err != nil {
		return nil, fmt.Errorf("connect to CircleCI: %w", err)
	}
	return client, nil
}

// githubChecks reads a PR's checks with the GitHub token, as the daemon
// does; with no token, every read fails so CI checks shows why.
func githubChecks() onboarding.PRChecks {
	var gh *github.Client
	if rc, err := config.Resolve("", "", false); err == nil && rc.GitHubToken != "" {
		gh, _ = github.New(github.Config{Token: rc.GitHubToken, BaseURL: rc.GitHubAPIURL})
	}
	return func(ctx context.Context, prURL string) (*onboarding.Checks, error) {
		if gh == nil {
			return nil, errors.New("no GitHub token to read the PR's checks")
		}
		owner, repo, number, err := parsePRURL(prURL)
		if err != nil {
			return nil, err
		}
		p, err := gh.FetchPRProgress(ctx, owner, repo, number)
		if err != nil || p == nil {
			return nil, err
		}
		return prChecks(p.CheckState), nil
	}
}

// prChecks is a PR's check rollup as onboarding reads it. GitHub's rollup
// gives one state, not counts, so it is marked Rollup: a failing or pending
// rollup counts one failed or pending check, shown without numbers, and
// success or no checks count as passed.
func prChecks(rollup string) *onboarding.Checks {
	c := &onboarding.Checks{State: rollup, Rollup: true}
	switch rollup {
	case "FAILURE", "ERROR":
		c.Failed = 1
	case "PENDING", "EXPECTED":
		c.Pending = 1
	case "SUCCESS":
		c.Passed = 1
	}
	return c
}

// parsePRURL is the owner, repository and number of a GitHub pull request
// URL, such as https://github.com/o/r/pull/7.
func parsePRURL(raw string) (owner, repo string, number int, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", 0, fmt.Errorf("parse PR URL %q: %w", raw, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", "", 0, fmt.Errorf("not a pull request URL: %s", raw)
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil {
		return "", "", 0, fmt.Errorf("not a pull request URL: %s", raw)
	}
	return parts[0], parts[1], n, nil
}
