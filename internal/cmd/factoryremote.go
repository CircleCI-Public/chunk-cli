package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
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
	// The rest are build flags that mean something on the host's own
	// 'factory build': forwarded through the exec script rather than acted on
	// here.
	reviewers               int
	noValidate              bool
	keepSidecars            bool
	model                   string
	implementerInstructions string
	implementTimeout        time.Duration
	reviewTimeout           time.Duration
	attempts                int
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
// baseline commit made there — then starts 'chunk factory build' on the host
// as one foreground exec, which is what keeps the sandbox awake, and follows
// its output. Any failure before that exec is submitted, and Ctrl-C before
// then, leaves nothing behind: the pool is destroyed, not kept, and the
// worktree removed. From the moment the exec is submitted the sidecar and the
// worktree belong to the run instead: a failure leaves them for inspection,
// and Ctrl-C only detaches, printing how to bring the work back later rather
// than cancelling or deleting anything.
func runFactoryBuildRemote(cmd *cobra.Command, opts factoryRemoteOptions) (err error) {
	ctx := cmd.Context()
	streams := iostream.FromCmd(cmd)
	statusFn := newStatusFunc(streams)

	if err := checkFactoryRemoteFlags(opts); err != nil {
		return err
	}
	printRunStartMessage(streams, opts.jsonOut, runStartMessage(prFlags{}, true, false))

	root, err := sessionProjectRoot(ctx)
	if err != nil {
		return err
	}
	cfg, err := checkFactoryRemotePreflight(root, opts.reviewsDir, opts.chunkBinary)
	if err != nil {
		return err
	}
	// The host runs from its own checkout, so --reviews reaches it relative
	// to the project root rather than as typed here, where it may be relative
	// to another directory or an absolute path on this machine.
	relReviews, err := factoryReviewsDir(root, opts.reviewsDir)
	if err != nil {
		return err
	}
	opts.reviewsDir = filepath.ToSlash(relReviews)

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
	// The run's implementer needs Claude on the host, so a missing credential
	// is found out before any sidecar is provisioned for it.
	cred, _, err := reviewCredential(rc)
	if err != nil {
		return err
	}
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
	} else if _, err := review.RunScript(ctx, sidecar.ClientExec, entry, review.InstallVersion(remoteReleaseTag()), factoryRemoteScriptLimit); err != nil {
		return &userError{msg: "Could not install chunk on the host sidecar.", err: err}
	}

	statusFn(iostream.LevelStep, "Making the baseline commit on the host...")
	if _, err := review.RunScript(ctx, sidecar.ClientExec, entry, factory.BaselineCommitScript(entry.RepoPath), factoryRemoteScriptLimit); err != nil {
		return &userError{msg: "Could not make the baseline commit on the host.", err: err}
	}

	statusFn(iostream.LevelStep, "Starting the run on the host...")
	script := remoteBuildScript(entry.RepoPath, resolvedOrgID, opts)
	env := review.DetachEnv(rc.CircleCIToken, review.Options{Credential: cred, BaseURL: rc.AnthropicBaseURL})
	commandID, err := entry.Client.SubmitExec(ctx, entry.ID, "sh", []string{"-c", script}, env)
	if err != nil {
		return &userError{msg: "Could not start the run on the host sidecar.", err: err}
	}
	// The run is going: from here, the sidecar and the worktree belong to it,
	// not to this command's own cleanup. A failure or a detach below leaves
	// them as they are.
	cleanupWorktree, destroyPool = false, false

	rec := factory.Record{
		RunID:    key,
		Prompt:   opts.prompt,
		Worktree: wt.Path,
		Branch:   wt.Branch,
		Baseline: wt.Baseline,
		Head:     wt.Head,
		Remote:   &factory.RemoteRun{SidecarID: entry.ID, CommandID: commandID, HostRepo: entry.RepoPath},
	}
	if err := factory.SaveRecord(dataDir, rec); err != nil {
		statusFn(iostream.LevelWarn, fmt.Sprintf("could not save the run's record: %v", err))
	}

	pullHint := "chunk factory pull " + key
	if err := followRemoteBuild(ctx, entry.Client, entry.ID, commandID, streams, statusFn, pullHint); err != nil {
		if errors.Is(err, errDetached) {
			return nil
		}
		return err
	}

	statusFn(iostream.LevelDone, fmt.Sprintf("The run finished on sidecar %s.", entry.ID))
	streams.ErrPrintf("Run %s to bring the work back.\n", pullHint)
	if opts.jsonOut {
		return iostream.PrintJSON(streams.Out, factoryRemoteResult{SidecarID: entry.ID})
	}
	streams.Printf("%s\n", entry.ID)
	return nil
}

// errDetached is what followRemoteBuild returns when Ctrl-C detaches from the
// host's run rather than it failing or finishing: nothing was stopped, and
// nothing was pulled.
var errDetached = errors.New("detached from the remote run")

// followRemoteBuild streams a host's 'chunk factory build' run, identified by
// commandID on sidecarID, until it ends: the host's stderr reaches the
// terminal as it arrives, and nothing from its stdout does. Ctrl-C detaches -
// it sends no cancel and pulls nothing - and returns errDetached, which a
// caller treats as success rather than failure. It is used both right after
// starting a remote run and by 'chunk factory pull' to wait out a run that
// has not ended yet, so there is exactly one place this behaves that way.
func followRemoteBuild(
	ctx context.Context, client *circleci.Client, sidecarID, commandID string,
	streams iostream.Streams, statusFn iostream.StatusFunc, pullHint string,
) error {
	statusFn(iostream.LevelStep, fmt.Sprintf("Following the run on sidecar %s. Ctrl-C detaches without stopping it.", sidecarID))
	stderrTail := newTailWriter(factoryRemoteStderrCap)
	res, streamErr := client.StreamOutput(ctx, commandID, "", func(stream string, data []byte) {
		if stream == circleci.StreamStderr {
			_, _ = streams.Err.Write(data)
			_, _ = stderrTail.Write(data)
		}
	})

	if streamErr != nil {
		// A stream broken by this process's own Ctrl-C detaches rather than
		// reporting a failure; any other break is a genuine lost connection,
		// whether or not Ctrl-C also happened to land at the same moment.
		if ctx.Err() != nil {
			streams.ErrPrintf("\nDetaching. The run continues on sidecar %s. Run %s once it's done to bring the work back.\n", sidecarID, pullHint)
			return errDetached
		}
		return &userError{
			msg:        fmt.Sprintf("Lost the host's output partway through the run on sidecar %s.", sidecarID),
			suggestion: fmt.Sprintf("The run may still be going. Inspect it with: chunk sidecar ssh --sidecar-id %s", sidecarID),
			err:        streamErr,
		}
	}
	if res.ExitCode != 0 {
		return &userError{
			msg:        fmt.Sprintf("The run failed on sidecar %s.", sidecarID),
			suggestion: fmt.Sprintf("Inspect it with: chunk sidecar ssh --sidecar-id %s", sidecarID),
			err:        fmt.Errorf("exit %d: %s", res.ExitCode, review.Tail(strings.TrimSpace(stderrTail.String()), factoryRemoteStderrTailLen)),
		}
	}
	return nil
}

// factoryRemoteStderrCap bounds how much of the host's stderr tailWriter
// keeps: the rest has already reached the terminal as it streamed, and is
// dropped from the front as more arrives, so a run that logs well past this
// many bytes over hours still reports the lines that explain a failure
// rather than some from early in the run.
const factoryRemoteStderrCap = 256 << 10

// factoryRemoteStderrTailLen is how much of that stderr a failure's error
// message carries.
const factoryRemoteStderrTailLen = 2000

// tailWriter keeps the last cap bytes written to it, evicting from the front
// as more arrives, so a long-running command's full output can be written to
// without growing without bound while what it ends with - the lines a
// failure needs - is never the part dropped.
type tailWriter struct {
	buf     []byte
	maxSize int
}

func newTailWriter(maxSize int) *tailWriter {
	return &tailWriter{maxSize: maxSize}
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if excess := len(w.buf) - w.maxSize; excess > 0 {
		w.buf = append(w.buf[:0], w.buf[excess:]...)
	}
	return len(p), nil
}

func (w *tailWriter) String() string { return string(w.buf) }

// remoteBuildScript is the script exec'd on the host: its own 'chunk factory
// build', run with 'exec' so this one process stays the tracked foreground
// command that keeps the sandbox awake. Every interpolated value - the host's
// checkout path, each flag's value and the prompt - is shell-escaped, since
// the exec API takes no shell of its own and rejects a command with spaces.
// Only the build flags that mean something on the host are forwarded; the
// rest (--continue, the log flags, --open-pr, --draft) are refused before
// this is ever built. The host is always given --no-open-pr: a run there
// would otherwise open a pull request whenever it could, and pushing from
// the host needs a GitHub credential in its exec environment that is not
// plumbed and is a decision of its own.
func remoteBuildScript(hostRepo, orgID string, opts factoryRemoteOptions) string {
	args := []string{
		"factory", "build", "--json", "--no-open-pr",
		"--org-id", orgID,
		"--max-attempts", strconv.Itoa(opts.attempts),
		"--implement-timeout", opts.implementTimeout.String(),
		"--review-timeout", opts.reviewTimeout.String(),
	}
	if opts.image != "" {
		args = append(args, "--image", opts.image)
	}
	if opts.reviewers > 0 {
		args = append(args, "--reviewers", strconv.Itoa(opts.reviewers))
	}
	if opts.reviewsDir != "" {
		args = append(args, "--reviews", opts.reviewsDir)
	}
	if opts.noValidate {
		args = append(args, "--no-validate")
	}
	if opts.keepSidecars {
		args = append(args, "--keep-sidecars")
	}
	if opts.model != "" {
		args = append(args, "--model", opts.model)
	}
	if opts.implementerInstructions != "" {
		args = append(args, "--implementer-instructions", opts.implementerInstructions)
	}
	args = append(args, "--", opts.prompt)

	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = sidecar.ShellEscape(a)
	}
	return fmt.Sprintf(`cd %s && exec "$HOME/chunk" %s`, sidecar.ShellEscape(hostRepo), strings.Join(quoted, " "))
}

// checkFactoryRemoteFlags refuses flag combinations --remote does not support
// yet: a continued run and the log options, since none of them have meaning
// without the local daemon that would otherwise carry them out. --open-pr and
// --draft are refused earlier, by checkFactoryBuildFlags.
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

// remoteReleaseTag is the release tag matching this build, which the host
// installs when no --chunk-binary is given. The prefix is trimmed first so a
// version set with or without its "v" names the same tag.
func remoteReleaseTag() string {
	return "v" + strings.TrimPrefix(version.Value, "v")
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
