package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newOrgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "org",
		Short:              "Manage CircleCI organizations",
		RunE:               groupRunE,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	}
	cmd.AddCommand(newOrgCreateCmd())
	cmd.AddCommand(newOrgListCmd())
	return cmd
}

func newOrgListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List CircleCI organizations",
		Long:  "List CircleCI organizations the authenticated user belongs to.\n\nUseful for finding your org ID to pass as --org-id or store with 'chunk config set orgID <id>'.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			insecureStorage, _ := cmd.Flags().GetBool("insecure-storage")
			rc, _ := config.Resolve("", "", insecureStorage)
			io := iostream.FromCmd(cmd)

			client, err := ensureCircleCIClient(cmd.Context(), cmd, rc, io, tui.PromptHidden)
			if err != nil {
				return err
			}

			collabs, err := client.ListCollaborations(cmd.Context())
			if err != nil {
				return &userError{
					msg:        "Could not list organizations.",
					suggestion: "Check your network connection.",
					err:        fmt.Errorf("list collaborations: %w", err),
				}
			}

			if jsonOut {
				if collabs == nil {
					collabs = []circleci.Collaboration{}
				}
				return iostream.PrintJSON(io.Out, collabs)
			}

			if len(collabs) == 0 {
				io.ErrPrintln(ui.Warning("No organizations found."))
				return nil
			}

			io.Printf("%-40s  %s\n", "ID", "NAME")
			for _, c := range collabs {
				io.Printf("%-40s  %s/%s\n", c.ID, c.VcsType, c.Name)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func newOrgCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new standalone CircleCI organization",
		Long:  "Create a new standalone CircleCI organization.\n\nRequires: CircleCI token (chunk auth login)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			insecureStorage, _ := cmd.Flags().GetBool("insecure-storage")
			rc, _ := config.Resolve("", "", insecureStorage)
			io := iostream.FromCmd(cmd)

			client, err := ensureCircleCIClient(cmd.Context(), cmd, rc, io, tui.PromptHidden)
			if err != nil {
				return &userError{
					msg:        "CircleCI authentication required.",
					suggestion: suggestionCircleCIAuth,
					err:        fmt.Errorf("resolve circleci client: %w", err),
				}
			}

			io.ErrPrintln(ui.Dim("Creating organization..."))
			org, err := client.CreateOrg(cmd.Context(), name)
			if err != nil {
				return &userError{
					msg: fmt.Sprintf("Failed to create organization %q.", name),
					err: fmt.Errorf("create org: %w", err),
				}
			}

			io.Println("")
			io.Println(ui.Success(fmt.Sprintf("Organization %q created.", org.Name)))
			io.Println("")
			io.Printf("  ID:   %s\n", org.ID)
			io.Printf("  Slug: %s\n", org.Slug)
			io.Println("")
			return nil
		},
	}
}
