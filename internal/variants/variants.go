package variants

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// Variant is one entry from the input JSON file.
type Variant struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Patch       string `json:"patch"`
}

// Result is one entry in the output JSON array.
type Result struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Killed      bool   `json:"killed"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	ExitCode    int    `json:"exit_code"`
	Error       string `json:"error,omitempty"`
}

// Options holds all configuration for running variants.
type Options struct {
	OrgID        string
	Image        string
	IdentityFile string
	AuthSock     string
	Workspace    string   // remote working directory, must be non-empty
	Parallel     int      // max concurrent sidecars (default 5)
	Commands     []string // shell commands to run on each sidecar in order
	StatusFn     iostream.StatusFunc
}

// Run executes all variants in parallel and returns results in input order.
// It only returns an error for fatal pre-flight failures; per-variant errors
// are captured in Result.Error.
func Run(ctx context.Context, client *circleci.Client, variants []Variant, opts Options) ([]Result, error) {
	if len(variants) == 0 {
		return nil, nil
	}
	if opts.Parallel <= 0 {
		opts.Parallel = 5
	}

	results := make([]Result, len(variants))
	sem := make(chan struct{}, opts.Parallel)

	g, gctx := errgroup.WithContext(ctx)
	for i, v := range variants {
		i, v := i, v
		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = runVariant(gctx, client, v, opts)
			return nil
		})
	}
	_ = g.Wait()
	return results, nil
}

func runVariant(ctx context.Context, client *circleci.Client, v Variant, opts Options) Result {
	base := Result{ID: v.ID, Description: v.Description}

	opts.StatusFn(iostream.LevelInfo, fmt.Sprintf("[%s] creating sidecar", v.ID))
	sc, err := sidecar.Create(ctx, client, opts.OrgID, variantSidecarName(v.ID), opts.Image)
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
	if err := sidecar.Sync(ctx, client, sc.ID, opts.IdentityFile, opts.AuthSock, opts.Workspace, opts.StatusFn); err != nil {
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
	for _, cmd := range opts.Commands {
		script := "cd " + sidecar.ShellEscape(opts.Workspace) + " && " + cmd
		result, err := sidecar.ExecOverSSH(ctx, session, "sh -c "+sidecar.ShellEscape(script), nil, nil)
		if err != nil {
			base.Error = fmt.Sprintf("exec: %v", err)
			return base
		}
		if result.ExitCode != 0 {
			base.Stdout = result.Stdout
			base.Stderr = result.Stderr
			base.ExitCode = result.ExitCode
			base.Killed = true
			opts.StatusFn(iostream.LevelDone, fmt.Sprintf("[%s] killed (exit %d)", v.ID, result.ExitCode))
			return base
		}
	}

	opts.StatusFn(iostream.LevelWarn, fmt.Sprintf("[%s] survived", v.ID))
	return base
}

// variantSidecarName produces a sidecar-safe name from a variant ID.
func variantSidecarName(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return "variant-" + b.String()
}
