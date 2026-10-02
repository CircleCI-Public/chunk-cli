package cmd

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// detachRequest carries what a detached review needs from the review command.
type detachRequest struct {
	client      *circleci.Client
	streams     iostream.Streams
	workDir     string
	promptsDir  string // as given on the command line; empty for the default
	orgID       string
	image       string
	destroyPool bool
	chunkBinary string // Linux chunk build to upload; empty to install the latest release
	circleToken string
	tokenSource string // where circleToken came from, for auth errors
	parallelism int
	model       string
	timeout     time.Duration
	jsonOut     bool
	opts        review.Options
}

// detachFlags is the review command's state that a detached run is built from.
type detachFlags struct {
	client      *circleci.Client
	streams     iostream.Streams
	workDir     string
	orgID       string
	image       string
	destroyPool bool
	chunkBinary string
	rc          config.ResolvedConfig
	cred        review.Credential
	parallelism int
	model       string
	timeout     time.Duration
	jsonOut     bool
}

// newDetachRequest builds a detachRequest from the review command's arguments
// (an optional prompts directory) and flags.
func newDetachRequest(args []string, f detachFlags) detachRequest {
	req := detachRequest{
		client:      f.client,
		streams:     f.streams,
		workDir:     f.workDir,
		orgID:       f.orgID,
		image:       f.image,
		destroyPool: f.destroyPool,
		chunkBinary: f.chunkBinary,
		circleToken: f.rc.CircleCIToken,
		tokenSource: f.rc.CircleCITokenSource,
		parallelism: f.parallelism,
		model:       f.model,
		timeout:     f.timeout,
		jsonOut:     f.jsonOut,
		opts:        review.Options{Credential: f.cred, BaseURL: f.rc.AnthropicBaseURL},
	}
	if len(args) == 1 {
		req.promptsDir = args[0]
	}
	return req
}

// detachedRun is what --detach --json prints.
type detachedRun struct {
	SidecarID string `json:"sidecar_id"`
	RunDir    string `json:"run_dir"`
}

// runReviewDetached hands the whole review to a primary sidecar and returns once
// it has started, so the laptop can close. The primary runs chunk review itself,
// with its own pool of reviewer sidecars, and leaves the report in a run
// directory on its home.
func runReviewDetached(ctx context.Context, req detachRequest) error {
	statusFn := newStatusFunc(req.streams)

	promptsDir, err := relativePromptsDir(req.workDir, req.promptsDir)
	if err != nil {
		return err
	}

	if err := checkNoActiveRun(ctx, req.client, req.workDir); err != nil {
		return err
	}

	statusFn(iostream.LevelStep, "Preparing the primary sidecar...")
	pool, err := newPool(ctx, req.client, sidecar.PoolOptions{
		Size:    1,
		Name:    review.PrimaryPoolName,
		OrgID:   req.orgID,
		Image:   req.image,
		WorkDir: req.workDir,
	}, "primary sidecar", req.tokenSource, statusFn)
	if err != nil {
		return err
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), poolCloseTimeout)
	defer cancel()
	defer pool.Close(closeCtx)

	if err := waitPoolReady(ctx, pool, "primary sidecar"); err != nil {
		return err
	}
	entry, err := pool.Acquire(ctx)
	if err != nil {
		return &userError{msg: "Could not check out the primary sidecar.", err: err}
	}
	defer pool.Release(entry)

	spec := review.DetachSpec{
		RunID:       time.Now().UTC().Format("20060102-150405"),
		RepoPath:    entry.RepoPath,
		OrgID:       req.orgID,
		PromptsDir:  promptsDir,
		Parallelism: req.parallelism,
		Model:       req.model,
		Timeout:     req.timeout,
		Image:       req.image,
		DestroyPool: req.destroyPool,
		Install:     review.InstallRelease,
	}
	if req.chunkBinary != "" {
		statusFn(iostream.LevelStep, "Uploading chunk to the primary sidecar...")
		if err := uploadBinary(ctx, req.client, entry.ID, req.chunkBinary, req.streams); err != nil {
			return &userError{msg: "Could not upload chunk to the primary sidecar.", err: err}
		}
		spec.Install = ""
	}

	statusFn(iostream.LevelStep, "Starting the review on the primary sidecar...")
	var stdout, stderr bytes.Buffer
	res, err := req.client.Exec(ctx, entry.ID, "sh", []string{"-c", review.DetachScript(spec)},
		review.DetachEnv(req.circleToken, req.opts),
		func(stream string, data []byte) {
			if stream == circleci.StreamStderr {
				_, _ = stderr.Write(data)
				return
			}
			_, _ = stdout.Write(data)
		})
	if err != nil {
		return &userError{msg: "Could not start the review on the primary sidecar.", err: err}
	}
	if res.ExitCode == review.ExitBadBinary {
		return &userError{
			msg:        "The chunk on the primary sidecar cannot run.",
			suggestion: "It must be a Linux build for the sidecar's architecture: GOOS=linux GOARCH=amd64 go build -o dist/chunk-linux . && chunk review --detach --chunk-binary dist/chunk-linux",
			hideDetail: true,
		}
	}
	if res.ExitCode == review.ExitNoReview {
		return &userError{
			msg:        "The chunk installed on the primary sidecar has no review command.",
			suggestion: "The latest release predates it. Build for Linux and pass it: GOOS=linux GOARCH=amd64 go build -o dist/chunk-linux . && chunk review --detach --chunk-binary dist/chunk-linux",
			hideDetail: true,
		}
	}
	if res.ExitCode != 0 {
		return &userError{
			msg:        "Starting the review on the primary sidecar failed.",
			suggestion: "Pass --chunk-binary with a Linux build if the release download is blocked.",
			err:        fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(stderr.String())),
		}
	}

	runDir := lastLine(stdout.String())
	if runDir == "" {
		return &userError{msg: "The primary sidecar did not report where the review is running.", hideDetail: true}
	}

	if err := saveDetachedState(req.workDir, detachedState{SidecarID: entry.ID, RunDir: runDir, StartedAt: time.Now()}); err != nil {
		// The review is already running; losing the shortcut must not hide that.
		statusFn(iostream.LevelWarn, fmt.Sprintf("could not remember this run: %v", err))
	}

	if req.jsonOut {
		return iostream.PrintJSON(req.streams.Out, detachedRun{SidecarID: entry.ID, RunDir: runDir})
	}
	req.streams.Printf("Review started on sidecar %s.\n\n", entry.ID)
	req.streams.Printf("It keeps running if you close this terminal. To read it later:\n")
	req.streams.Printf("  chunk review results\n")
	return nil
}

// lastLine returns the last non-empty line of out, trimmed. The run directory
// is the script's last line, and a whole line keeps a path with spaces intact.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// relativePromptsDir turns the prompts directory argument into a path inside
// the checkout, since the primary only has the synced tree.
func relativePromptsDir(workDir, dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	abs := dir
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(workDir, dir)
	}
	rel, err := filepath.Rel(workDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", newUserError("The prompts directory must be inside the project when using --detach.").
			withCode("command.invalid_args").
			withSuggestion(fmt.Sprintf("Move it under %s, or omit it to use %s.", workDir, review.DefaultDir)).
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return filepath.ToSlash(rel), nil
}

// uploadBinary streams a gzipped chunk build to $HOME/chunk on the sidecar.
func uploadBinary(ctx context.Context, client *circleci.Client, sidecarID, path string, streams iostream.Streams) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	pr, pw := io.Pipe()
	go func() {
		gz := gzip.NewWriter(pw)
		_, err := io.Copy(gz, f)
		if cerr := gz.Close(); err == nil {
			err = cerr
		}
		_ = pw.CloseWithError(err)
	}()
	defer func() { _ = pr.Close() }()

	return sidecar.SSH(ctx, client, sidecarID, []string{"sh", "-c", review.UploadInstall}, nil, streams, pr)
}

// detachedState is what a detached review leaves in the project so that
// 'chunk review results' can find it again without any arguments.
type detachedState struct {
	SidecarID string    `json:"sidecar_id"`
	RunDir    string    `json:"run_dir"`
	StartedAt time.Time `json:"started_at"`
}

func detachedStatePath(workDir string) string {
	return filepath.Join(workDir, ".chunk", "review-run.json")
}

func saveDetachedState(workDir string, st detachedState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal detached run: %w", err)
	}
	path := detachedStatePath(workDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write detached run: %w", err)
	}
	return nil
}

func loadDetachedState(workDir string) (detachedState, error) {
	var st detachedState
	data, err := os.ReadFile(detachedStatePath(workDir))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("parse %s: %w", detachedStatePath(workDir), err)
	}
	return st, nil
}

func newReviewResultsCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:          "results",
		Short:        "Print the report of the last detached review",
		Long:         "Print the report of the last review started with --detach in this project, or say how far it has got.",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)

			workDir, err := os.Getwd()
			if err != nil {
				return err
			}
			st, err := loadDetachedState(workDir)
			if err != nil {
				return &userError{
					msg:        "No detached review found in this project.",
					suggestion: "Start one with: chunk review --detach",
					hideDetail: true,
					err:        err,
				}
			}

			rc, _ := config.Resolve("", "", insecureStorageFlag(cmd))
			client, err := ensureCircleCIClient(ctx, cmd, rc, streams, ui.PromptHidden)
			if err != nil {
				return err
			}
			return printDetachedResults(ctx, client, streams, st, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// execRead runs the read script for a detached run on its primary sidecar and
// returns what it printed and its exit code.
func execRead(ctx context.Context, client *circleci.Client, st detachedState) (string, int, error) {
	var stdout bytes.Buffer
	res, err := client.Exec(ctx, st.SidecarID, "sh", []string{"-c", review.ReadScript(st.RunDir)}, nil,
		func(stream string, data []byte) {
			if stream != circleci.StreamStderr {
				_, _ = stdout.Write(data)
			}
		})
	if err != nil {
		return "", 0, err
	}
	return stdout.String(), res.ExitCode, nil
}

// checkNoActiveRun refuses to start a detached review while this project's
// previous one is still running on its primary sidecar. The two would share the
// primary's checkout and reviewer pool, and the new run would replace the local
// record of the old one, so its report could no longer be fetched. If the
// previous run cannot be read, for instance because its sidecar has expired,
// there is nothing left to protect and a new run may start.
func checkNoActiveRun(ctx context.Context, client *circleci.Client, workDir string) error {
	st, err := loadDetachedState(workDir)
	if err != nil {
		return nil
	}
	out, code, err := execRead(ctx, client, st)
	if err != nil || code != 0 {
		return nil
	}
	status, err := review.ParseRead(out)
	if err != nil || status.State != review.RunRunning {
		return nil
	}
	return &userError{
		msg: fmt.Sprintf("A detached review is still running on sidecar %s (started %s ago).",
			st.SidecarID, time.Since(st.StartedAt).Round(time.Second)),
		suggestion: "Read it with 'chunk review results', or wait for it to finish before starting another.",
		hideDetail: true,
	}
}

func printDetachedResults(ctx context.Context, client *circleci.Client, streams iostream.Streams, st detachedState, jsonOut bool) error {
	out, code, err := execRead(ctx, client, st)
	if err != nil {
		return &userError{
			msg:        fmt.Sprintf("Could not reach the primary sidecar %s.", st.SidecarID),
			suggestion: "It may have expired. Run 'chunk review --detach' to start again.",
			err:        err,
		}
	}
	if code != 0 {
		return &userError{msg: "Could not read the review from the primary sidecar.", hideDetail: true,
			err: fmt.Errorf("exit %d", code)}
	}
	status, err := review.ParseRead(out)
	if err != nil {
		return &userError{msg: "Could not read the review from the primary sidecar.", err: err}
	}

	switch status.State {
	case review.RunMissing:
		return &userError{
			msg:        "The primary sidecar no longer has this review.",
			suggestion: "Run 'chunk review --detach' to start again.",
			hideDetail: true,
		}
	case review.RunDied:
		return &userError{
			msg:        "The review on the primary sidecar stopped before it finished.",
			suggestion: "Its process is gone and it left no exit code, so the sidecar may have restarted. Run 'chunk review --detach' to start again." + logSuffix(status.Body),
			hideDetail: true,
		}
	case review.RunRunning:
		streams.Printf("Still running (started %s ago).\n", time.Since(st.StartedAt).Round(time.Second))
		if log := strings.TrimSpace(status.Body); log != "" {
			streams.Printf("\n%s\n", log)
		}
		return nil
	}

	var report reviewReport
	if err := json.Unmarshal([]byte(status.Body), &report); err != nil {
		return &userError{
			msg:        fmt.Sprintf("The review finished with exit code %d but left no report.", status.ExitCode),
			suggestion: "Log from the primary sidecar:\n" + strings.TrimSpace(status.Log),
			hideDetail: true,
			err:        err,
		}
	}
	if jsonOut {
		if err := iostream.PrintJSON(streams.Out, report); err != nil {
			return fmt.Errorf("write reviews: %w", err)
		}
		return detachedFailure(report, status)
	}
	results := make([]review.Result, 0, len(report.Reviews))
	for _, r := range report.Reviews {
		results = append(results, review.Result{
			Prompt:    r.Prompt,
			SidecarID: r.SidecarID,
			Output:    r.Output,
			Error:     r.Error,
			Duration:  time.Duration(r.DurationSeconds * float64(time.Second)),
		})
	}
	printReviews(streams, results)
	return detachedFailure(report, status)
}

// detachedFailure turns a finished run into the error the local 'chunk review'
// would have returned: failed reviews, or a run that exited nonzero even though
// its report shows no failures, such as a pass that stopped early.
func detachedFailure(report reviewReport, status review.RunStatus) error {
	if report.Failed > 0 {
		return &userError{
			msg:        fmt.Sprintf("%d of %d review(s) failed.", report.Failed, len(report.Reviews)),
			errMsg:     "reviews failed",
			hideDetail: true,
		}
	}
	if status.ExitCode != 0 {
		return &userError{
			msg:        fmt.Sprintf("The review exited with code %d.", status.ExitCode),
			suggestion: "Log from the primary sidecar:" + logSuffix(status.Log),
			errMsg:     "review exited nonzero",
			hideDetail: true,
		}
	}
	return nil
}

// logSuffix formats the tail of a run's log for a suggestion, or nothing if empty.
func logSuffix(log string) string {
	log = strings.TrimSpace(log)
	if log == "" {
		return ""
	}
	return "\n" + log
}

// checkPromptsSynced refuses a detached review whose prompts the primary would
// not receive. Sync carries tracked and untracked files but not ignored ones, so
// a prompts directory that is ignored, or partly so, is present here and absent
// there. want is the number of prompts loaded locally.
func checkPromptsSynced(ctx context.Context, workDir, dir string, want int) error {
	out, err := (gitexec.Runner{Dir: workDir}).Output(ctx, "ls-files", "--cached", "--others", "--exclude-standard", "--", dir)
	if err != nil {
		// Not a git checkout the sync could use either; it reports that itself.
		return nil
	}
	got := 0
	for _, f := range strings.Split(string(out), "\n") {
		if f == "" || filepath.Dir(f) != filepath.Clean(relToWork(workDir, dir)) {
			continue
		}
		if review.IsPromptFile(f) {
			got++
		}
	}
	if got >= want {
		return nil
	}
	return newUserError("Some review prompts would not reach the primary sidecar.").
		withCode("command.invalid_args").
		withSuggestion(fmt.Sprintf("Only files git tracks or could track are synced, and %s has ignored prompts. Stop ignoring them, or run without --detach.", dir)).
		withExitCode(ExitBadArgs).
		withoutDetail()
}

func relToWork(workDir, dir string) string {
	if !filepath.IsAbs(dir) {
		return dir
	}
	rel, err := filepath.Rel(workDir, dir)
	if err != nil {
		return dir
	}
	return rel
}
