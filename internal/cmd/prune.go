package cmd

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/localprune"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newPruneCmd() *cobra.Command {
	var orgID, before string
	var force, local, yes bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete your sidecars",
		Long: `Delete your sidecar instances for the given org.

By default the API deletes sidecars created more than 1 hour ago.
Pass --before to extend the cutoff (e.g. --before 24h).

Pass --local to delete chunk's leftover local files instead: state left
behind at paths chunk no longer reads as its primary source. It never
touches remote sidecars. Without --yes it only lists what it would remove.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if local {
				return runLocalPrune(cmd, yes)
			}
			io := iostream.FromCmd(cmd)
			insecureStorage := insecureStorageFlag(cmd)
			rc, _ := config.Resolve("", "", insecureStorage)
			client, err := ensureCircleCIClient(cmd.Context(), cmd, rc, io, ui.PromptHidden)
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			resolvedOrgID, err := resolveOrgID(orgID, cwd, orgPicker(cmd.Context(), client, rc.CircleCITokenSource, io))
			if err != nil {
				return err
			}

			var cutoff *time.Time
			if before != "" {
				d, parseErr := time.ParseDuration(before)
				if parseErr != nil || d <= 0 {
					return &userError{
						msg:        fmt.Sprintf("Invalid --before value %q.", before),
						suggestion: "Use a Go duration like 2h or 24h.",
						errMsg:     fmt.Sprintf("invalid duration %q", before),
					}
				}
				t := time.Now().UTC().Add(-d)
				cutoff = &t
			}

			if !force {
				if nonInteractive() {
					return errNoForce("prune sidecars")
				}
				confirmed, err := ui.Confirm("Delete all matching sidecars?", false)
				if errors.Is(err, ui.ErrNoTTY) {
					return errNoForce("prune sidecars")
				}
				if err != nil || !confirmed {
					io.ErrPrintln("Cancelled.")
					return nil
				}
			}

			deleted, err := client.PruneSidecars(cmd.Context(), resolvedOrgID, cutoff)
			if err != nil {
				if errors.Is(err, circleci.ErrNotAuthorized) {
					return &userError{
						msg:        "Not authorized to prune sidecars.",
						suggestion: suggestionReauth,
						err:        err,
					}
				}
				return &userError{
					msg:        "Could not prune sidecars.",
					suggestion: suggestionNetworkRetry,
					err:        err,
				}
			}

			if deleted == 0 {
				io.ErrPrintln(ui.Dim("No sidecars deleted"))
				return nil
			}

			io.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Deleted %d sidecar(s)", deleted)))

			cleared, cerr := sidecar.ClearActiveByOrg(resolvedOrgID)
			if cerr != nil {
				io.ErrPrintf("Warning: could not clear active sidecar state: %v\n", cerr)
			}
			if cleared > 0 {
				io.ErrPrintln("Active sidecar cleared")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&before, "before", "", "Delete sidecars created more than this long ago (e.g. 2h, 24h; default: 1h)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&local, "local", false, "Delete chunk's leftover local files instead of remote sidecars")
	cmd.Flags().BoolVar(&yes, "yes", false, "Actually delete; without it, only list what --local would remove")

	return cmd
}

// runLocalPrune lists, or with yes deletes, chunk's leftover local files.
func runLocalPrune(cmd *cobra.Command, yes bool) error {
	io := iostream.FromCmd(cmd)
	plan, err := localprune.Scan(cmd.Context())
	if err != nil {
		return fmt.Errorf("scan local state: %w", err)
	}
	if len(plan.Items) == 0 {
		io.ErrPrintln(ui.Dim("Nothing to prune"))
		return nil
	}
	for _, it := range plan.Items {
		io.Printf("%s\n", it.Path)
		io.ErrPrintln(ui.Dim("  " + it.Reason))
	}
	if !yes {
		io.ErrPrintf("\n%d item(s) would be removed. Re-run with --local --yes to delete them.\n", len(plan.Items))
		return nil
	}
	if err := plan.Apply(cmd.Context()); err != nil {
		return &userError{msg: "Could not remove all local leftovers.", err: err}
	}
	io.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Removed %d item(s)", len(plan.Items))))
	return nil
}
