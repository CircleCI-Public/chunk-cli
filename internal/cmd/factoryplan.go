package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/factoryplan"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

func newFactoryPlanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan [request]",
		Short: "Plan a change in factory, on one of your factory workers",
		Long: `Plan a change in factory. The screen asks for a title and a request (the
argument, or what you type when there is none) and creates the plan in factory
with them. chunk then hands the plan to your factory worker, which plans it
with Claude Code; what the worker is doing is shown beside the plan. Comment on
the plan until it is right, then press ctrl+s to approve it, and the worker
builds it, through to a pull request. Follow the build in chunk ui.

Needs factory daemon running with FACTORY_PLAN_STORE=circleci, as the CircleCI
user chunk is signed in as, on a machine of yours. It needs no Claude Code on
this machine.`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stopHangup := untilHangup(cmd.Context())
			defer stopHangup()
			streams := iostream.FromCmd(cmd)
			if err := ui.RequireStdoutTTY(); err != nil {
				return fmt.Errorf("factory plan requires a TTY")
			}
			root, api, err := factoryPlanPreflight(ctx)
			if err != nil {
				return err
			}

			// planCtx bounds the screen's reads of the plan.
			planCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			store := factoryplan.NewStore(api, "", planRepo(ctx, root))

			var request string
			if len(args) == 1 {
				request = args[0]
			}
			// The branch is only for the header; a repo without one has none.
			branch, _ := gitutil.CurrentBranchIn(root)
			screen := plan.New(planCtx, store, request).
				WithTerminal(lipgloss.HasDarkBackground(os.Stdin, os.Stdout), false).
				WithBranch(branch)
			p := tea.NewProgram(plan.Screen{Model: screen}, tea.WithContext(ctx))
			final, err := p.Run()
			if err != nil {
				return err
			}
			left := final.(plan.Screen).Model
			reportPlanLeft(streams, store.ID(), left.Approved, left.Worker)
			return nil
		},
	}
	return cmd
}

// factoryPlanPreflight is what chunk factory plan checks before its screen
// opens: the git repository it is run in, that the project can be built from
// (checkPlanProject), and the CircleCI client the plan is made with.
func factoryPlanPreflight(ctx context.Context) (string, *circleci.Client, error) {
	root, err := sessionProjectRoot(ctx)
	if err != nil {
		return "", nil, err
	}
	if err := checkPlanProject(ctx, root); err != nil {
		return "", nil, err
	}
	api, err := factoryPlanClient()
	if err != nil {
		return "", nil, err
	}
	return root, api, nil
}

// sessionProjectRoot names the project to work on: the git repository
// containing the working directory.
func sessionProjectRoot(ctx context.Context) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine working directory: %w", err)
	}
	top := gitutil.TopLevelCtx(ctx, dir)
	if top == "" {
		return "", newUserError(fmt.Sprintf("%s is not inside a git repository.", dir)).
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	// A build needs an origin to clone and push; refuse before anything starts.
	if _, err := gitremote.URL(ctx, top, "origin"); err != nil {
		return "", newUserError("This project has no git remote named origin.").
			withSuggestion("Add one with: git remote add origin <url>").
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	return config.CanonicalProjectRoot(top), nil
}

// checkPlanProject reports why a plan's build cannot start in root, as one
// sentence for the dashboard, or nil when it can.
func checkPlanProject(ctx context.Context, root string) error {
	if _, err := gitremote.URL(ctx, root, "origin"); err != nil {
		return errors.New("this project has no git remote named origin; add one with git remote add origin <url>")
	}
	cfg, err := config.LoadProjectConfig(root)
	if err != nil {
		return errors.New(dashboardMessage(&userError{msg: msgValidateNotConfigured, suggestion: suggestionRunInit, err: err}))
	}
	dir := filepath.Join(root, review.DefaultDir)
	_, _, err = factory.LoadChecks(dir, true, cfg, false)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, factory.ErrNothingToCheck):
		return errors.New(dashboardMessage(&userError{
			msg:        "Nothing for factory to check a plan's build with.",
			suggestion: fmt.Sprintf("Add one .md or .txt file per review to %s, or add validation commands with 'chunk init'.", review.DefaultDir),
		}))
	}
	return errors.New(dashboardMessage(&userError{msg: fmt.Sprintf("Could not read review prompts from %s.", dir), err: err}))
}

// factoryPlanClient resolves the CircleCI client the planning screen's store
// talks to, from the developer's saved token. It never prompts: the
// planning screen is not the place to start an interactive login, so a
// missing token comes back as a plain error naming how to fix it.
func factoryPlanClient() (*circleci.Client, error) {
	rc, err := config.ResolveCircleCI(false)
	if err != nil {
		return nil, fmt.Errorf("load the CircleCI config: %w", err)
	}
	client, err := authprompt.ResolveCircleCIClient(rc, nil)
	if errors.Is(err, authprompt.ErrNeedsAuth) {
		return nil, newUserError("CircleCI authentication is required to plan with the factory.").
			withCode("auth.circleci_token_required").
			withSuggestion(suggestionCircleCIAuth).
			withExitCode(ExitAuthError).
			withoutDetail()
	}
	if err != nil {
		return nil, fmt.Errorf("connect to CircleCI: %w", err)
	}
	return client, nil
}

// reportPlanLeft says, as the screen closes, where the plan with ID planID was
// left: not approved, approved with worker building it, or approved with none.
func reportPlanLeft(streams iostream.Streams, planID string, approved bool, worker string) {
	switch {
	case planID == "":
	case !approved:
		streams.ErrPrintf("The plan was not approved. It is plan %s in factory.\n", planID)
	case worker == "":
		streams.ErrPrintf("Plan %s is approved; no worker is building it. Press R in chunk ui to claim it.\n", planID)
	default:
		streams.ErrPrintf("Plan %s is approved; worker %s is building it.\n", planID, worker)
		if showHints(streams, false) {
			streams.ErrPrintln(watchStartHint)
		}
	}
}

// planRepo is the GitHub repository, as owner/name, of the project at root,
// which a plan made there records; "" when its origin is not on GitHub or it
// has none.
func planRepo(ctx context.Context, root string) string {
	org, name, err := gitremote.DetectOrgAndRepoCtx(ctx, root)
	if err != nil {
		return ""
	}
	return org + "/" + name
}
