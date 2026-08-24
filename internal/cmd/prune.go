package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newPruneCmd() *cobra.Command {
	var orgID string

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete your sidecars (older than 1 hour)",
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

			deleted, err := client.PruneSidecars(cmd.Context(), resolvedOrgID)
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

	return cmd
}
