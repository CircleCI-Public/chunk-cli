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
		Short: "Delete all sidecars",
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

			sidecars, err := client.ListSidecars(cmd.Context(), resolvedOrgID, false)
			if err != nil {
				if errors.Is(err, circleci.ErrNotAuthorized) {
					return &userError{
						msg:        "Not authorized to list sidecars.",
						suggestion: suggestionReauth,
						err:        err,
					}
				}
				return &userError{
					msg:        "Could not list sidecars.",
					suggestion: suggestionNetworkRetry,
					err:        err,
				}
			}

			active, _ := sidecar.LoadActive(cmd.Context())
			deletedActive := false
			for _, s := range sidecars {
				if err := client.DeleteSidecar(cmd.Context(), s.ID); err != nil {
					io.ErrPrintf("Warning: could not delete sidecar %s (%s): %v\n", s.Name, s.ID, err)
					continue
				}
				io.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Deleted sidecar %s (%s)", s.Name, s.ID)))
				if active != nil && active.SidecarID == s.ID {
					deletedActive = true
				}
			}
			if deletedActive {
				if cerr := sidecar.ClearActive(cmd.Context()); cerr != nil {
					io.ErrPrintf("Warning: could not clear active sidecar state: %v\n", cerr)
				} else {
					io.ErrPrintln("Active sidecar cleared")
				}
			}
			if len(sidecars) == 0 {
				io.ErrPrintln(ui.Dim("No sidecars found"))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")

	return cmd
}
