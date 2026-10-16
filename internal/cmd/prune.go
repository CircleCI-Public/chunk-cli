package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newPruneCmd() *cobra.Command {
	var orgID, before string
	var force, skipInUse bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete your sidecars",
		Long: `Delete your sidecar instances for the given org.

Without --before every sidecar you own is deleted, whatever its age, including
ones still running a command. Pass --before to only delete sidecars created more
than this long ago (e.g. --before 24h).

Pass --skip-in-use to keep any sidecar chunk's state on this machine refers to:
active sidecars, pools, factory runs and detached reviews. Sidecars in use from
another machine are not known here and are still deleted.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if skipInUse && before != "" {
				return &userError{
					msg:        "--skip-in-use cannot be combined with --before.",
					suggestion: "The sidecar list carries no creation time, so pick one.",
					errMsg:     "--skip-in-use with --before",
				}
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

			if skipInUse {
				return pruneSkippingInUse(cmd.Context(), io, client, resolvedOrgID)
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
	cmd.Flags().StringVar(&before, "before", "", "Only delete sidecars created more than this long ago (e.g. 2h, 24h)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&skipInUse, "skip-in-use", false, "Keep sidecars that chunk's state on this machine refers to")

	return cmd
}

// pruneSkippingInUse deletes the caller's sidecars one at a time, keeping any
// that local state refers to. The deleted ones are not in local state, so
// there is no active sidecar to clear.
func pruneSkippingInUse(ctx context.Context, io iostream.Streams, client *circleci.Client, orgID string) error {
	inUse, err := sidecar.InUseIDs()
	if err != nil {
		return fmt.Errorf("read local sidecar state: %w", err)
	}
	sidecars, err := client.ListSidecars(ctx, orgID, false)
	if err != nil {
		if errors.Is(err, circleci.ErrNotAuthorized) {
			return &userError{msg: "Not authorized to list sidecars.", suggestion: suggestionReauth, err: err}
		}
		return &userError{msg: "Could not list sidecars.", suggestion: suggestionNetworkRetry, err: err}
	}

	deleted := 0
	for _, s := range sidecars {
		if inUse[s.ID] {
			io.ErrPrintf("Kept %s (%s): in use on this machine\n", s.Name, s.ID)
			continue
		}
		if err := client.DeleteSidecar(ctx, s.ID); err != nil {
			io.ErrPrintf("Warning: could not delete sidecar %s (%s): %v\n", s.Name, s.ID, err)
			continue
		}
		deleted++
	}

	if deleted == 0 {
		io.ErrPrintln(ui.Dim("No sidecars deleted"))
		return nil
	}
	io.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Deleted %d sidecar(s)", deleted)))
	return nil
}
