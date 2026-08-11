package variants

import (
	"context"
	"fmt"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// namePrefix marks every sidecar this package creates. Variant sidecars are
// deliberately absent from the active-sidecar file, so the reaper in
// internal/sidecar cannot see them; the prefix is what makes them findable by
// SweepOrphans instead.
const namePrefix = "variant-"

// orphanAfter is how long a variant sidecar must have existed before a later
// run's pre-flight sweep may delete it.
//
// Two runs at once — two worktrees, two repos, an agent driving one while a
// human drives another — is a normal shape for this command, and the sweep
// cannot ask the API who owns what. So it goes by age instead: a sidecar young
// enough to belong to a live run is left alone, and only one old enough that no
// run could still be waiting on it is collected. The cost of waiting is some
// billed time on a genuinely stranded sidecar; the cost of not waiting is
// destroying someone else's in-flight run.
const orphanAfter = 2 * time.Hour

// Variant is one entry from the input JSON file.
type Variant struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Patch       string `json:"patch"`
}

// Command is one validation command to run on each variant's sidecar.
//
// Run must already be expanded by the caller: a command carrying an unexpanded
// {{CHANGED_PACKAGES}} exits non-zero in the shell, which this package would
// otherwise read as a killed mutant.
type Command struct {
	Name    string
	Run     string
	Timeout int // seconds; 0 means no limit
}

// Result is one entry in the output JSON array.
//
// Killed and Error are mutually exclusive. Killed means the test suite ran and
// failed, which is a caught mutant. A non-empty Error means the mutant was never
// assessed — the sidecar died, the patch would not apply, the command was not
// on the image, the suite never finished. Those must not be reported as kills:
// an environmental failure that reads as a caught mutant turns a broken run into
// a clean bill of health, which is the one direction this tool must not fail in.
type Result struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Killed      bool   `json:"killed"`

	// Command names the validation command that produced the verdict, so a kill
	// can be traced to the command that caught it and a failure to the command
	// that could not run.
	Command  string `json:"command,omitempty"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// Options holds all configuration for running variants.
type Options struct {
	OrgID        string
	Image        string
	IdentityFile string
	AuthSock     string
	Workspace    string    // remote working directory, must be non-empty
	Parallel     int       // max concurrent sidecars (default 5)
	Commands     []Command // commands to run on each sidecar in order
	StatusFn     iostream.StatusFunc
}

// Run executes all variants in parallel and returns results in input order.
// It only returns an error for fatal pre-flight failures; per-variant errors
// are captured in Result.Error.
//
// Run installs its own SIGINT/SIGTERM handler for the duration of the call.
// Without it an interrupt kills the process outright, every deferred
// DeleteSidecar is skipped, and each in-flight sidecar is left running and
// billing. Catching the signal turns it into a context cancellation that
// unwinds through those defers instead.
func Run(ctx context.Context, client *circleci.Client, variants []Variant, opts Options) ([]Result, error) {
	if len(variants) == 0 {
		return nil, nil
	}
	if opts.Parallel <= 0 {
		opts.Parallel = 5
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Stamped once for the whole run so every sidecar this run creates carries
	// the same start time, and a later run's sweep can date it.
	token := runToken(time.Now())

	results := make([]Result, len(variants))
	sem := make(chan struct{}, opts.Parallel)

	g, gctx := errgroup.WithContext(ctx)
	for i, v := range variants {
		i, v := i, v
		g.Go(func() error {
			// Acquiring the slot must observe cancellation too: on interrupt the
			// queued variants have not created anything yet, and booting a fresh
			// sidecar on the way out is the exact leak the signal handler exists
			// to prevent.
			select {
			case sem <- struct{}{}:
			case <-gctx.Done():
				results[i] = Result{ID: v.ID, Description: v.Description, Error: "cancelled before start"}
				return nil
			}
			defer func() { <-sem }()
			results[i] = runVariant(gctx, client, v, sidecarName(token, v.ID), opts)
			return nil
		})
	}
	_ = g.Wait()
	return results, nil
}

func runVariant(ctx context.Context, client *circleci.Client, v Variant, name string, opts Options) Result {
	base := Result{ID: v.ID, Description: v.Description}

	opts.StatusFn(iostream.LevelInfo, fmt.Sprintf("[%s] creating sidecar", v.ID))
	sc, err := sidecar.Create(ctx, client, opts.OrgID, name, opts.Image)
	if err != nil {
		base.Error = fmt.Sprintf("create sidecar: %v", err)
		return base
	}
	defer func() {
		// Use a fresh context so cleanup runs even when the parent is cancelled.
		if err := client.DeleteSidecar(context.Background(), sc.ID); err != nil {
			opts.StatusFn(iostream.LevelWarn, fmt.Sprintf("[%s] could not delete sidecar %s: %v", v.ID, sc.ID, err))
		}
	}()

	opts.StatusFn(iostream.LevelInfo, fmt.Sprintf("[%s] syncing", v.ID))
	if err := sidecar.SyncEphemeral(ctx, client, sc.ID, opts.IdentityFile, opts.AuthSock, opts.Workspace, opts.StatusFn); err != nil {
		base.Error = fmt.Sprintf("sync: %v", err)
		return base
	}

	session, err := sidecar.OpenSession(ctx, client, sc.ID, opts.IdentityFile, opts.AuthSock)
	if err != nil {
		base.Error = fmt.Sprintf("open session: %v", err)
		return base
	}

	if v.Patch != "" {
		opts.StatusFn(iostream.LevelInfo, fmt.Sprintf("[%s] applying patch", v.ID))
		applyCmd := "git -C " + sidecar.ShellEscape(opts.Workspace) + " apply"
		applyResult, err := sidecar.ExecOverSSH(ctx, session, applyCmd, strings.NewReader(v.Patch), nil)
		if err != nil {
			base.Error = fmt.Sprintf("apply patch: %v", err)
			return base
		}
		if applyResult.ExitCode != 0 {
			base.Error = "patch did not apply"
			return base
		}
	}

	opts.StatusFn(iostream.LevelInfo, fmt.Sprintf("[%s] running commands", v.ID))
	return runCommands(ctx, session, v, base, opts)
}

// runCommands runs each validation command on the variant's sidecar in order and
// returns the verdict. The first command to exit non-zero decides it, and the
// rest are skipped: the mutant is already caught, and the remaining commands
// would only be running against known-broken code.
func runCommands(ctx context.Context, session *sidecar.Session, v Variant, base Result, opts Options) Result {
	for _, c := range opts.Commands {
		script := "cd " + sidecar.ShellEscape(opts.Workspace) + " && " + c.Run
		result, timedOut, err := execCommand(ctx, session, "sh -c "+sidecar.ShellEscape(script), c.Timeout)

		base.Command = c.Name
		switch {
		case timedOut:
			// The suite never returned, so nothing was proven either way. Returning
			// here is also what stops a runaway mutant from holding a billed sidecar
			// and a parallel slot for the rest of the run: the deferred delete in
			// runVariant fires on the way out.
			base.Error = fmt.Sprintf("%s: timed out after %ds", c.Name, c.Timeout)
			opts.StatusFn(iostream.LevelWarn, fmt.Sprintf("[%s] %s", v.ID, base.Error))
			return base
		case err != nil:
			base.Error = fmt.Sprintf("%s: exec: %v", c.Name, err)
			return base
		case result.ExitCode == 0:
			continue
		}

		base.Stdout = result.Stdout
		base.Stderr = result.Stderr
		base.ExitCode = result.ExitCode

		if reason := shellFailure(result.ExitCode); reason != "" {
			base.Error = fmt.Sprintf("%s: %s (exit %d)", c.Name, reason, result.ExitCode)
			opts.StatusFn(iostream.LevelWarn, fmt.Sprintf("[%s] not assessed: %s", v.ID, base.Error))
			return base
		}

		base.Killed = true
		opts.StatusFn(iostream.LevelDone, fmt.Sprintf("[%s] killed by %s (exit %d)", v.ID, c.Name, result.ExitCode))
		return base
	}

	// Every command passed, so there is no command to attribute the outcome to;
	// the loop left the last one's name behind.
	base.Command = ""
	opts.StatusFn(iostream.LevelWarn, fmt.Sprintf("[%s] survived", v.ID))
	return base
}

// execCommand runs script on the sidecar, giving up after timeoutSecs seconds
// when that is set.
//
// The deadline is enforced here rather than through the context because
// ExecOverSSH's underlying ssh session.Run does not observe cancellation once
// the command is running — a context deadline would expire silently and the call
// would keep waiting. Racing the call against a timer means abandoning the SSH
// session, which is harmless: the caller deletes the sidecar immediately after,
// and that is what actually stops the remote command and the billing.
func execCommand(
	ctx context.Context, session *sidecar.Session, script string, timeoutSecs int,
) (res *sidecar.ExecResult, timedOut bool, err error) {
	if timeoutSecs <= 0 {
		res, err = sidecar.ExecOverSSH(ctx, session, script, nil, nil)
		return res, false, err
	}

	type outcome struct {
		res *sidecar.ExecResult
		err error
	}
	// Buffered so the abandoned goroutine can finish and exit once the SSH
	// connection drops with the sidecar, rather than blocking forever on send.
	done := make(chan outcome, 1)
	go func() {
		r, e := sidecar.ExecOverSSH(ctx, session, script, nil, nil)
		done <- outcome{r, e}
	}()

	timer := time.NewTimer(time.Duration(timeoutSecs) * time.Second)
	defer timer.Stop()

	select {
	case o := <-done:
		return o.res, false, o.err
	case <-timer.C:
		return nil, true, fmt.Errorf("timed out after %ds", timeoutSecs)
	case <-ctx.Done():
		// The caller interrupted the run. Not a timeout: there is no verdict to
		// report either way, and the distinction matters because a timeout is a
		// property of the mutant while cancellation is a property of the run.
		return nil, false, ctx.Err()
	}
}

// shellFailure reports why an exit code means the command never ran, or ""
// when the code is a genuine failure of the command itself.
//
// 127 and 126 are the shell's own failures rather than the command's: not found,
// and found but not executable. Both are what a snapshot missing the project's
// tooling looks like, and it looks the same for every variant in the run — so
// reading them as kills would report a whole broken run as a perfectly covered
// codebase.
func shellFailure(code int) string {
	switch code {
	case 126:
		return "command found but not executable on the sidecar"
	case 127:
		return "command not found on the sidecar"
	}
	return ""
}

// runToken encodes a run's start time as the per-run segment of its sidecar
// names. Base 36 keeps it short and inside the [a-z0-9-] set names allow.
func runToken(start time.Time) string {
	return strconv.FormatInt(start.Unix(), 36)
}

// sidecarName produces a sidecar-safe name from a run token and a variant ID.
// The token makes the name unique per run, so two concurrent runs over the same
// variant IDs do not collide, and datable, so SweepOrphans can tell an
// abandoned sidecar from one a live run still owns.
func sidecarName(token, id string) string {
	var b strings.Builder
	b.WriteString(namePrefix)
	b.WriteString(token)
	b.WriteByte('-')
	for _, r := range strings.ToLower(id) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// tokenEpoch is the earliest time a run token may decode to, and tokenSkew is
// how far past now it may decode and still be believed.
//
// The bounds are what make a token distinguishable from anything else in its
// position. Base 36 accepts every lowercase letter as a digit, so the sanitized
// variant ID in a name this package did not write parses to a number too:
// "variant-mut-001" reads as 1970 and "variant-timeout-fix" as the year 3970.
// Neither is a plausible run, and only a plausible one is datable. The skew
// covers ordinary clock differences between the machine that named a sidecar and
// the machine sweeping it.
var tokenEpoch = time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

const tokenSkew = 24 * time.Hour

// startedAt recovers the run start time encoded in a variant sidecar name.
// ok is false for any name this package did not write, or whose token does not
// decode to a plausible time. Such a name is never swept: a sweep that cannot
// prove a sidecar is abandoned must leave it alone.
func startedAt(name string, now time.Time) (time.Time, bool) {
	rest, ok := strings.CutPrefix(name, namePrefix)
	if !ok {
		return time.Time{}, false
	}
	token, _, ok := strings.Cut(rest, "-")
	if !ok || token == "" {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(token, 36, 64)
	if err != nil {
		return time.Time{}, false
	}
	started := time.Unix(secs, 0)
	if started.Before(tokenEpoch) || started.After(now.Add(tokenSkew)) {
		return time.Time{}, false
	}
	return started, true
}

// SweepOrphans deletes sidecars in the org left behind by an earlier variants
// run. It is a backstop, not the primary cleanup: Run deletes its own sidecars
// and the signal handler covers interrupts, but a crash or a lost network
// connection can still strand one, and nothing else will ever collect it.
//
// Only names carrying namePrefix and older than orphanAfter are touched, so a
// concurrent run's in-flight sidecars survive. Deletion failures are reported
// and skipped rather than aborting the sweep — a stale entry the API refuses to
// delete must not block the run the caller actually asked for.
func SweepOrphans(ctx context.Context, client *circleci.Client, orgID string, status iostream.StatusFunc) int {
	existing, err := client.ListSidecars(ctx, orgID, false)
	if err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not list sidecars to sweep orphans: %v", err))
		return 0
	}

	now := time.Now()
	cutoff := now.Add(-orphanAfter)
	swept := 0
	for _, sc := range existing {
		if !strings.HasPrefix(sc.Name, namePrefix) {
			continue
		}
		started, ok := startedAt(sc.Name, now)
		if !ok {
			status(iostream.LevelWarn, fmt.Sprintf(
				"leaving variant sidecar %s in place: its name carries no run timestamp, so it cannot be shown to be abandoned", sc.Name))
			continue
		}
		if started.After(cutoff) {
			// Young enough that a concurrent run may still be using it.
			continue
		}
		if err := client.DeleteSidecar(ctx, sc.ID); err != nil {
			status(iostream.LevelWarn, fmt.Sprintf("could not delete orphaned sidecar %s (%s): %v", sc.Name, sc.ID, err))
			continue
		}
		status(iostream.LevelInfo, fmt.Sprintf("swept orphaned sidecar %s", sc.Name))
		swept++
	}
	return swept
}
