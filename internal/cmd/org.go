package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
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

			client, err := ensureCircleCIClient(cmd.Context(), cmd, rc, io, ui.PromptHidden)
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

			client, err := ensureCircleCIClient(cmd.Context(), cmd, rc, io, ui.PromptHidden)
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

const suggestionNoOrgs = "Create one with `chunk org create <name>`, pass --org-id, or ask an admin to invite you to an existing organization."

// promptOrgName asks for the name of a new organization. It returns
// ui.ErrNoTTY without printing anything when there is no terminal to prompt
// on. The prompt renders to stdout, so a piped stdout counts as no terminal.
// Swapped out in tests.
var promptOrgName = func(streams iostream.Streams) (string, error) {
	if nonInteractive() || !term.IsTerminal(int(os.Stdin.Fd())) || ui.RequireStdoutTTY() != nil {
		return "", ui.ErrNoTTY
	}
	streams.ErrPrintln("You don't belong to any CircleCI organizations yet. Let's create one.")
	return ui.PromptText("Organization name", "")
}

// createFirstOrg offers to create an organization for an account that belongs
// to none, which is the state of every account straight after signup. It
// returns the new organization's ID. Without a terminal it returns an error
// pointing at `chunk org create`, which needs no prompt.
func createFirstOrg(ctx context.Context, client *circleci.Client, streams iostream.Streams) (string, error) {
	name, err := promptOrgName(streams)
	switch {
	case errors.Is(err, ui.ErrNoTTY):
		return "", &userError{
			code:       "org.none_found",
			msg:        "No organizations found.",
			suggestion: suggestionNoOrgs,
			err:        fmt.Errorf("no organizations found for current user"),
		}
	case errors.Is(err, ui.ErrCancelled):
		return "", &userError{
			msg:        "No organization created.",
			suggestion: suggestionNoOrgs,
			err:        err,
			hideDetail: true,
		}
	case err != nil:
		return "", fmt.Errorf("prompt org name: %w", err)
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return "", &userError{
			msg:        "No organization created.",
			suggestion: suggestionNoOrgs,
			errMsg:     "no organization name entered",
		}
	}

	streams.ErrPrintln(ui.Dim("Creating organization..."))
	org, err := client.CreateOrg(ctx, name)
	if err != nil {
		return "", &userError{
			code:       "org.create_failed",
			msg:        fmt.Sprintf("Failed to create organization %q.", name),
			suggestion: "Check whether the name is already taken, or try another with `chunk org create <name>`.",
			err:        fmt.Errorf("create org: %w", err),
		}
	}
	streams.ErrPrintln(ui.ErrSuccess(fmt.Sprintf("Organization %q created.", org.Name)))
	return org.ID, nil
}

// ensureOrgAfterSignup follows a signup by creating the new account's first
// organization, so the first sidecar command does not fail on having none. The
// signup itself has already succeeded, so problems here are reported as
// warnings rather than errors.
func ensureOrgAfterSignup(ctx context.Context, streams iostream.Streams, baseURL, token string) {
	client, err := circleci.NewClient(circleci.Config{Token: token, BaseURL: baseURL})
	if err != nil {
		return
	}
	collabs, err := client.ListCollaborations(ctx)
	if err != nil {
		streams.ErrPrintln(ui.ErrWarning(fmt.Sprintf("Could not check your organizations: %v", err)))
		return
	}
	if len(collabs) > 0 {
		return
	}
	streams.ErrPrintln("")
	if _, err := createFirstOrg(ctx, client, streams); err != nil {
		var ue *userError
		if errors.As(err, &ue) {
			streams.ErrPrintln(ui.ErrWarning(ue.msg))
			detail := ue.detail
			if detail == "" {
				detail = ue.Error()
			}
			if !ue.hideDetail && detail != "" {
				streams.ErrPrintln(ui.Dim(detail))
			}
			streams.ErrPrintln("Suggestion: " + ue.suggestion)
			return
		}
		streams.ErrPrintln(ui.ErrWarning(err.Error()))
	}
}
