package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/signoff"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// envSignoffLocal turns on running the commands on this machine when it is "1".
const envSignoffLocal = "CHUNK_SIGNOFF_LOCAL"

// exitInterrupted is the status a run stopped by Ctrl-C exits with, as a shell
// reports a process killed by SIGINT.
const exitInterrupted = 130

func newSignoffCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:          "signoff",
		Short:        "Run the validation commands on a snapshot of the working tree and report each as a factory check run",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := runSignoff(cmd, force)
			if mapped := outdatedSidecarAPI(err); mapped != nil {
				return mapped
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Run every command, even one an earlier run on this tree signed off")
	return cmd
}

func runSignoff(cmd *cobra.Command, force bool) error {
	ctx := cmd.Context()
	streams := iostream.FromCmd(cmd)
	statusFn := newStatusFunc(streams)

	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	// The repository's root rather than the current directory: the snapshot and
	// its checkout are of the whole repository, whatever directory this ran in.
	dir := gitutil.TopLevelCtx(ctx, workDir)
	if dir == "" {
		return newUserError("Not in a git repository.").
			withCode("signoff.no_repo").
			withSuggestion("Run 'chunk signoff' from inside the repository whose working tree you are signing off.").
			withExitCode(ExitBadArgs).
			wrapMsg("not a git repository")
	}

	cfg, err := signoffConfig(dir)
	if err != nil {
		return err
	}
	// Signoff does nothing unless the repository opts in, or when it has nothing
	// to run: a caller may run it in every repository.
	if !cfg.Signoff {
		streams.Printf("signoff is off for this repository: set \"signoff\": true in .chunk/config.json to turn it on\n")
		return nil
	}
	commands := factory.ValidationCommands(cfg.Commands)
	if len(commands) == 0 {
		streams.Printf("nothing to sign off: .chunk/config.json has no validation commands\n")
		return nil
	}
	key, repo, err := signoffOrigin(ctx, dir)
	if err != nil {
		return err
	}

	rc, _ := config.ResolveCircleCI(insecureStorageFlag(cmd))
	client, err := ensureCircleCIClient(ctx, cmd, rc, streams, ui.PromptHidden)
	if err != nil {
		return err
	}

	orgID, err := resolveOrgID("", dir, orgPicker(ctx, client, rc.CircleCITokenSource, streams))
	if err != nil {
		return signoffOrgError(err)
	}

	// A submodule's uncommitted changes are not in the snapshot, which records
	// only the commit it is at. Warn and carry on; a status that fails here will
	// fail the snapshot too, with its own error.
	if dirty, err := gitutil.DirtySubmodules(dir); err == nil {
		for _, path := range dirty {
			statusFn(iostream.LevelWarn, fmt.Sprintf("submodule %s has uncommitted changes; signoff records only its commit, so they are not part of the key or the checkout", path))
		}
	}

	tree, err := gitutil.SnapshotTree(dir)
	if err != nil {
		return fmt.Errorf("snapshot the working tree: %w", err)
	}
	checkout, cleanup, err := gitutil.MaterializeTree(dir, tree)
	if err != nil {
		return fmt.Errorf("check out the snapshot: %w", err)
	}
	defer cleanup()
	key += tree

	// A command whose newest run on this tree succeeded is not run again. A
	// lookup that fails costs only the saving: running is always correct.
	reused := map[string]bool{}
	if !force {
		runs, err := client.ListCheckRuns(ctx, circleci.CheckRunFilter{OrgID: orgID, Repo: repo, Key: key})
		switch {
		case ctx.Err() != nil:
			return signoffInterrupted(streams)
		case err != nil:
			statusFn(iostream.LevelWarn, fmt.Sprintf("could not look up earlier check runs, so every command runs: %v", err))
		default:
			reused = signoff.Reusable(commands, runs)
		}
	}

	// With every command reused nothing runs, so no sidecar is reaped, created or
	// acquired.
	target := signoffTarget{finish: func() {}, release: func() {}}
	if len(reused) < len(commands) {
		// Command output is progress, so it goes to stderr and leaves stdout for
		// the lines that say how each command ended.
		outputStreams := iostream.Streams{Out: streams.Err, Err: streams.Err}
		if os.Getenv(envSignoffLocal) == "1" {
			target = localSignoffTarget(checkout, outputStreams)
		} else {
			target, err = sidecarSignoffTarget(ctx, client, rc, orgID, dir, checkout, statusFn, streams, outputStreams)
			if err != nil {
				return err
			}
		}
	}
	defer target.release()

	streams.Printf("signing off %s%s\n", shortKey(key), target.header)
	res, runErr := signoff.Run(ctx, signoff.Options{
		Commands: commands,
		Reused:   reused,
		Key:      key,
		Exec:     target.exec,
		Dir:      target.dir,
		Post: func(ctx context.Context, run circleci.CheckRun) error {
			run.OrgID, run.Repo = orgID, repo
			_, err := client.CreateCheckRun(ctx, run)
			return err
		},
		Status:  statusFn,
		Streams: streams,
	})
	target.finish()

	if errors.Is(runErr, context.Canceled) {
		return signoffInterrupted(streams)
	}
	var reusedNote string
	if res.Reused > 0 {
		reusedNote = fmt.Sprintf(" (%d from an earlier run)", res.Reused)
	}
	// Only a run in which every command passed and was posted signs off: a
	// failed post leaves a command with no run, however it ended.
	if runErr == nil && res.Passed == res.Total {
		streams.Printf("signed off %d of %d%s\n", res.Passed, res.Total, reusedNote)
	} else {
		streams.Printf("not signed off: %d of %d passed%s\n", res.Passed, res.Total, reusedNote)
	}
	if res.Reused < res.Total {
		streams.Printf("  ran on a clean checkout of the snapshot: files .gitignore excludes were not synced\n")
	}
	if runErr != nil {
		if authErr := notAuthorized("report check runs", rc.CircleCITokenSource, runErr); authErr != nil {
			return authErr
		}
		return newUserError("Could not sign off every command.").
			withCode("signoff.incomplete").
			withDetail(runErr.Error()).
			withExitCode(ExitAPIError).
			wrap(runErr)
	}
	if res.Failed > 0 {
		return &silentExitError{code: ExitGeneral}
	}
	return nil
}

// signoffInterrupted reports that Ctrl-C stopped signoff and returns the error
// that exits as a shell reports an interrupted process.
func signoffInterrupted(streams iostream.Streams) error {
	streams.ErrPrintln(ui.ErrWarning("interrupted"))
	return &silentExitError{code: exitInterrupted}
}

// signoffConfig returns dir's project config, or an error when dir has none or
// it cannot be read.
func signoffConfig(dir string) (*config.ProjectConfig, error) {
	cfg, err := config.LoadProjectConfig(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, &userError{
			msg:        "No commands to sign off.",
			blocked:    true,
			suggestion: "Add a command that runs on a sidecar to .chunk/config.json, or run 'chunk init' to detect and configure them.",
			errMsg:     "no commands to sign off",
		}
	case err != nil:
		return nil, &userError{msg: msgCouldNotLoadConfig, suggestion: configFilePermHint, err: err}
	}
	return cfg, nil
}

// configOrEmpty is dir's project config, or an empty one when it has none.
func configOrEmpty(dir string) *config.ProjectConfig {
	if cfg, err := config.LoadProjectConfig(dir); err == nil {
		return cfg
	}
	return &config.ProjectConfig{}
}

// signoffOrigin reads origin: keyPrefix is the part of a run's key that names
// the repository and ends in what the tree SHA completes, <repo identity>@tree:,
// and repo is origin's GitHub OWNER/NAME.
func signoffOrigin(ctx context.Context, dir string) (keyPrefix, repo string, err error) {
	remote, err := gitremote.URL(ctx, dir, "origin")
	if err != nil {
		return "", "", newUserError("This repository has no origin remote.").
			withCode("signoff.no_origin").
			withSuggestion("Add one with 'git remote add origin <url>'. Sign-off names a repository by its origin, so every clone of it shares one key.").
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	identity, err := gitremote.Identity(remote)
	if err != nil {
		return "", "", newUserError("This repository's origin remote has no host.").
			withCode("signoff.origin_has_no_host").
			withDetail(fmt.Sprintf("origin is %q.", remote)).
			withSuggestion("Point origin at the repository's URL on its host: a local path names a directory on one machine, which no other runner could share.").
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	owner, name, err := gitremote.ParseRemoteURL(remote)
	if err != nil {
		return "", "", newUserError("This repository's origin is not on GitHub.").
			withCode("signoff.origin_not_github").
			withDetail(fmt.Sprintf("origin is %q.", remote)).
			withSuggestion("Sign-off reports to factory check runs, which take only GitHub repositories.").
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	return identity + "@tree:", owner + "/" + name, nil
}

// shortKey shortens the tree SHA in key to eight characters for display.
func shortKey(key string) string {
	prefix, tree, ok := strings.Cut(key, "@tree:")
	if !ok || len(tree) <= 8 {
		return key
	}
	return prefix + "@tree:" + tree[:8]
}

// signoffTarget is where signoff's commands run.
type signoffTarget struct {
	exec signoff.ExecFunc
	// dir is the directory the commands run in.
	dir string
	// header ends the line that says what is being signed off.
	header string
	// finish runs once the commands are done; release runs when signoff ends.
	finish, release func()
}

// localSignoffTarget runs the commands on this machine, in checkout.
func localSignoffTarget(checkout string, streams iostream.Streams) signoffTarget {
	return signoffTarget{
		exec: func(ctx context.Context, script string) (string, string, int, error) {
			cmd := exec.CommandContext(ctx, "sh", "-c", script)
			cmd.Stdout = streams.Out
			cmd.Stderr = streams.Err
			// A command's children may hold its output open after it is killed.
			cmd.WaitDelay = 2 * time.Second
			err := cmd.Run()
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return "", "", exitErr.ExitCode(), nil
			}
			return "", "", 0, err
		},
		dir:     checkout,
		header:  " locally",
		finish:  func() {},
		release: func() {},
	}
}

// sidecarSignoffTarget runs the commands on a sidecar that checkout is synced
// to. dir is the real project, which names the sidecar and keys its state.
func sidecarSignoffTarget(ctx context.Context, client *circleci.Client, rc config.ResolvedConfig, orgID, dir, checkout string, statusFn iostream.StatusFunc, streams, outputStreams iostream.Streams) (signoffTarget, error) {
	reapAbandonedSidecars(ctx, client, dir, statusFn, streams)
	activeSidecar, _ := sidecar.LoadActive(ctx)

	// The checkout is what is synced to the sidecar and what the commands run in:
	// it is the tree the key names, and it cannot move while they run.
	opts := &validateOpts{orgID: orgID}
	pool, err := prepareValidationTarget(ctx, client, opts, resolveImage("", configOrEmpty(dir)), rc.CircleCITokenSource, 1, activeSidecar, statusFn, checkout, dir, streams)
	if err != nil {
		return signoffTarget{}, err
	}
	entry, err := pool.Acquire(ctx)
	if err != nil {
		return signoffTarget{}, fmt.Errorf("acquire validation sidecar: %w", err)
	}

	target := sidecar.Target{Client: entry.Client, SidecarID: entry.ID, Workdir: entry.RepoPath}
	execFn, dest, err := target.ReadyExecRunner(ctx, checkout, remoteExecEnv(rc.CircleCIToken, nil), outputStreams)
	if err != nil {
		pool.Release(entry)
		var workspaceErr *sidecar.WorkspaceNotFoundError
		if errors.As(err, &workspaceErr) {
			return signoffTarget{}, missingWorkspace(entry.ID, entry.RepoPath, "signoff", err)
		}
		return signoffTarget{}, unreachableSidecar(entry.ID, "signoff", err)
	}
	return signoffTarget{
		exec: execFn,
		dir:  dest,
		finish: func() {
			if err := saveActivePoolState(ctx, pool, opts.orgID); err != nil {
				statusFn(iostream.LevelWarn, fmt.Sprintf("could not save active sidecar: %v", err))
			}
		},
		release: func() { pool.Release(entry) },
	}, nil
}

// signoffOrgError rewrites the org picker's suggestions that name --org-id,
// which signoff does not have.
func signoffOrgError(err error) error {
	var ue *userError
	if !errors.As(err, &ue) {
		return err
	}
	if !strings.Contains(ue.suggestion, "--org-id") {
		return err
	}
	c := *ue
	switch {
	case ue.msg == msgOrgNoTTY:
		return c.withSuggestion("Run 'chunk org list' to find your org ID, then set it with 'chunk config set orgID <id>' or CIRCLECI_ORG_ID.").
			withExitCode(ExitBadArgs)
	case ue.suggestion == suggestionNoOrgs:
		return c.withSuggestion("Create one with `chunk org create <name>`, set CIRCLECI_ORG_ID, or ask an admin to invite you to an existing organization.").
			withExitCode(ExitBadArgs)
	case ue.msg == msgListOrgsFailed:
		return c.withSuggestion("Check your network connection.")
	case ue.msg == "Could not select an organization.":
		return c.withSuggestion("Set CIRCLECI_ORG_ID instead.")
	}
	return err
}
