package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/version"
)

// factoryRemoteOptions carries the build command's flags that
// runFactoryBuildRemote needs. The prompt has already been read from the
// argument or stdin by the time --remote is handled.
type factoryRemoteOptions struct {
	prompt      string
	orgID       string
	image       string
	chunkBinary string
	reviewsDir  string
	continueRun string
	logOn       bool
	logFile     string
	verbose     bool
	jsonOut     bool
}

// factoryRemoteResult is chunk factory build --remote's --json output.
type factoryRemoteResult struct {
	SidecarID string `json:"sidecar_id"`
}

// factoryRemoteScriptLimit caps what a script run on the host sidecar during
// provisioning may print: an install's output, or a commit hash.
const factoryRemoteScriptLimit = 64 << 10

// runFactoryBuildRemote provisions a sidecar of its own for one factory build
// run — a worktree here, a one-sidecar pool, chunk installed on it, and the
// baseline commit made there — so the run can later go in the foreground on
// that sidecar instead of the local daemon. It does not start the run itself;
// that is a later change. Any failure, and Ctrl-C before the run starts,
// leaves nothing behind: the pool is destroyed, not kept, and the worktree
// removed.
func runFactoryBuildRemote(cmd *cobra.Command, opts factoryRemoteOptions) (err error) {
	ctx := cmd.Context()
	streams := iostream.FromCmd(cmd)
	statusFn := newStatusFunc(streams)

	if err := checkFactoryRemoteFlags(opts); err != nil {
		return err
	}

	root, err := sessionProjectRoot(ctx)
	if err != nil {
		return err
	}
	cfg, err := checkFactoryRemotePreflight(root, opts.reviewsDir, opts.chunkBinary)
	if err != nil {
		return err
	}

	key := remoteRunKey()
	dataDir, err := config.RepoDataDir(root)
	if err != nil {
		return fmt.Errorf("find chunk's data directory for the repository: %w", err)
	}
	wt, err := factory.CreateWorktree(ctx, root, filepath.Join(dataDir, "factory", key), key, opts.prompt)
	if err != nil {
		return &userError{msg: "Could not create the run's worktree.", err: err}
	}
	cleanupWorktree := true
	defer func() {
		if !cleanupWorktree {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if rmErr := wt.Remove(cleanupCtx, root); rmErr != nil {
			statusFn(iostream.LevelWarn, fmt.Sprintf("could not remove the run's worktree %s: %v", wt.Path, rmErr))
		}
	}()

	rc, _ := config.Resolve("", "", insecureStorageFlag(cmd))
	client, err := ensureCircleCIClient(ctx, cmd, rc, streams, ui.PromptHidden)
	if err != nil {
		return err
	}
	orgID := opts.orgID
	if orgID == "" {
		orgID = cfg.OrgID
	}
	resolvedOrgID, err := resolveOrgID(orgID, root, orgPicker(ctx, client, rc.CircleCITokenSource, streams))
	if err != nil {
		return err
	}
	image := opts.image
	if image == "" {
		image = resolveImage("", cfg)
	}

	poolName := "factory-remote-" + key
	statePath, err := remotePoolStatePath(root, poolName)
	if err != nil {
		return fmt.Errorf("resolve the pool's state path: %w", err)
	}

	statusFn(iostream.LevelStep, "Preparing a sidecar for the remote run...")
	pool, err := newPool(ctx, client, sidecar.PoolOptions{
		Size:      1,
		Name:      poolName,
		OrgID:     resolvedOrgID,
		Image:     image,
		WorkDir:   wt.Path,
		StatePath: statePath,
	}, "remote factory sidecar", rc.CircleCITokenSource, statusFn)
	if err != nil {
		return err
	}
	destroyPool := true
	defer func() { closePool(ctx, pool, destroyPool, statusFn) }()

	if err := waitPoolReady(ctx, pool, "remote factory sidecar"); err != nil {
		return err
	}
	entry, err := pool.Acquire(ctx)
	if err != nil {
		return &userError{msg: "Could not check out the host sidecar.", err: err}
	}
	defer pool.Release(entry)

	statusFn(iostream.LevelStep, "Installing chunk on the host...")
	if opts.chunkBinary != "" {
		if err := uploadBinary(ctx, client, entry.ID, opts.chunkBinary, streams); err != nil {
			return &userError{msg: "Could not upload chunk to the host sidecar.", err: err}
		}
	} else if _, err := review.RunScript(ctx, sidecar.ClientExec, entry, review.InstallVersion("v"+version.Value), factoryRemoteScriptLimit); err != nil {
		return &userError{msg: "Could not install chunk on the host sidecar.", err: err}
	}

	statusFn(iostream.LevelStep, "Making the baseline commit on the host...")
	if _, err := review.RunScript(ctx, sidecar.ClientExec, entry, factory.BaselineCommitScript(entry.RepoPath), factoryRemoteScriptLimit); err != nil {
		return &userError{msg: "Could not make the baseline commit on the host.", err: err}
	}

	cleanupWorktree, destroyPool = false, false
	if opts.jsonOut {
		return iostream.PrintJSON(streams.Out, factoryRemoteResult{SidecarID: entry.ID})
	}
	streams.Printf("%s\n", entry.ID)
	return nil
}

// checkFactoryRemoteFlags refuses flag combinations --remote does not support
// yet: a continued run, and the log options, since none of them have meaning
// without the local daemon that would otherwise carry them out.
func checkFactoryRemoteFlags(opts factoryRemoteOptions) error {
	switch {
	case opts.continueRun != "":
		return remoteUnsupportedFlag("--continue")
	case opts.logOn:
		return remoteUnsupportedFlag("--log")
	case opts.logFile != "":
		return remoteUnsupportedFlag("--log-file")
	case opts.verbose:
		return remoteUnsupportedFlag("--verbose")
	}
	return nil
}

func remoteUnsupportedFlag(name string) error {
	return newUserError(fmt.Sprintf("%s is not supported with --remote yet.", name)).
		withCode("command.invalid_flags").
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// checkFactoryRemotePreflight checks what the host's own 'factory build' will
// need before any sidecar is provisioned: the project's config and its review
// prompts directory. A dev build with no --chunk-binary is refused here too,
// since it names no release that could be installed on the host.
func checkFactoryRemotePreflight(root, reviewsDir, chunkBinary string) (*config.ProjectConfig, error) {
	cfg, err := config.LoadProjectConfig(root)
	if err != nil {
		return nil, &userError{msg: msgValidateNotConfigured, suggestion: suggestionRunInit, err: err, blocked: true}
	}
	relReviews, err := factoryReviewsDir(root, reviewsDir)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, review.DefaultDir)
	if relReviews != "" {
		dir = filepath.Join(root, relReviews)
	}
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		return nil, &userError{
			msg:        fmt.Sprintf("No review prompts directory at %s.", dir),
			suggestion: "Run 'chunk factory bootstrap' to write review prompts there.",
			blocked:    true,
			hideDetail: true,
		}
	}
	if chunkBinary == "" && version.Value == "dev" {
		return nil, &userError{
			msg: "This dev build of chunk has no release to install on the host.",
			suggestion: "Build for Linux and pass it: GOOS=linux GOARCH=amd64 go build -o dist/chunk-linux . && " +
				"chunk factory build --remote --chunk-binary dist/chunk-linux",
			blocked:    true,
			hideDetail: true,
		}
	}
	return cfg, nil
}

// remoteRunKey names a remote run's worktree, branch and pool. The "r-"
// prefix keeps it from ever colliding with a local run's ID, whatever
// factory.NewRunID returns.
func remoteRunKey() string {
	return "r-" + factory.NewRunID()
}

// remotePoolStatePath is where a remote run's one-sidecar pool keeps its
// state, keyed by the project root rather than by the run's own worktree:
// the worktree is removed on failure, and the pool's state belongs to the
// project either way.
func remotePoolStatePath(root, name string) (string, error) {
	dataDir, err := config.ProjectDataDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "pools", name+".json"), nil
}
