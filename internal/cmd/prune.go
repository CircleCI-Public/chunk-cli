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
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newPruneCmd() *cobra.Command {
	var orgID, before string

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete your sidecars",
		Long: `Delete your sidecar instances for the given org.

By default the API deletes sidecars created more than 1 hour ago.
Pass --before to extend the cutoff (e.g. --before 24h).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			insecureStorage := insecureStorageFlag(cmd)
			rc, _ := config.Resolve("", "", insecureStorage)
			client, err := ensureCircleCIClient(cmd.Context(), cmd, rc, io, tui.PromptHidden)
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			resolvedOrgID, err := resolveOrgID(orgID, cwd, orgPicker(cmd.Context(), client))
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
				t := time.Now().Add(-d)
				cutoff = &t
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

			active, _ := sidecar.LoadActive(cmd.Context())
			if active != nil {
				if cerr := sidecar.ClearActive(cmd.Context()); cerr != nil {
					io.ErrPrintf("Warning: could not clear active sidecar state: %v\n", cerr)
				} else {
					io.ErrPrintln("Active sidecar cleared")
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&before, "before", "", "Delete sidecars created more than this long ago (e.g. 2h, 24h; default: 1h)")

	return cmd
}
