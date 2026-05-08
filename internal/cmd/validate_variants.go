package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/variants"
)

func newValidateVariantsCmd() *cobra.Command {
	var name, orgID, image, identityFile, workdir string
	var parallel int

	cmd := &cobra.Command{
		Use:          "variants <variants-file>",
		Short:        "Run validation commands against code variants on parallel sidecars",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			streams := iostream.FromCmd(cmd)
			ctx := cmd.Context()

			data, err := os.ReadFile(args[0])
			if err != nil {
				return &userError{
					msg: fmt.Sprintf("Could not read variants file %q.", args[0]),
					err: err,
				}
			}
			var vs []variants.Variant
			if err := json.Unmarshal(data, &vs); err != nil {
				return &userError{
					msg:        "Invalid variants file.",
					suggestion: "Expected a JSON array of {id, description, patch} objects.",
					err:        err,
				}
			}
			if len(vs) == 0 {
				streams.ErrPrintln("No variants to run.")
				return nil
			}

			workDir, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg, err := config.LoadProjectConfig(workDir)
			if err != nil {
				return &userError{
					msg:        "No validate commands configured.",
					suggestion: "Run 'chunk init' first.",
					err:        err,
				}
			}

			var cmds []config.Command
			if name != "" {
				c := cfg.FindCommand(name)
				if c == nil {
					return &userError{
						msg:    fmt.Sprintf("Command %q is not configured.", name),
						errMsg: fmt.Sprintf("command %q not found", name),
					}
				}
				cmds = []config.Command{*c}
			} else {
				for _, c := range cfg.Commands {
					if c.Remote {
						cmds = append(cmds, c)
					}
				}
			}
			if len(cmds) == 0 {
				return &userError{
					msg:        "No remote commands configured.",
					suggestion: "Mark at least one command as remote in .chunk/config.json, or use --name to specify a command.",
					errMsg:     "no remote commands configured",
				}
			}

			client, err := ensureCircleCIClient(ctx, streams, tui.PromptHidden)
			if err != nil {
				return err
			}

			if orgID == "" && cfg.OrgID != "" {
				orgID = cfg.OrgID
			}
			resolvedOrgID, err := resolveOrgID(orgID, orgPicker(ctx, client))
			if err != nil {
				return err
			}

			if image == "" && cfg.Validation != nil {
				image = cfg.Validation.SidecarImage
			}

			workspace := resolveVariantsWorkspace(ctx, workdir, workDir)

			cmdStrings := make([]string, len(cmds))
			for i, c := range cmds {
				cmdStrings[i] = c.Run
			}

			authSock := os.Getenv(config.EnvSSHAuthSock)
			results, err := variants.Run(ctx, client, vs, variants.Options{
				OrgID:        resolvedOrgID,
				Image:        image,
				IdentityFile: identityFile,
				AuthSock:     authSock,
				Workspace:    workspace,
				Parallel:     parallel,
				Commands:     cmdStrings,
				StatusFn:     newStatusFunc(streams),
			})
			if err != nil {
				return &userError{msg: "Variants run failed.", err: err}
			}

			killed := 0
			for _, r := range results {
				if r.Killed {
					killed++
				}
			}
			statusFn := newStatusFunc(streams)
			statusFn(iostream.LevelDone, fmt.Sprintf("%d/%d variants killed", killed, len(results)))

			out, err := json.MarshalIndent(results, "", "  ")
			if err != nil {
				return fmt.Errorf("encode results: %w", err)
			}
			streams.Printf("%s\n", out)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Validate command name to run (default: all remote commands)")
	cmd.Flags().IntVar(&parallel, "parallel", 5, "Max concurrent sidecars")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&image, "image", "", "Snapshot image ID (default: validation.sidecarImage from config)")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Remote working directory")

	return cmd
}

// resolveVariantsWorkspace derives the remote workspace path for variant workers.
// All workers share the same workspace to avoid concurrent writes to the active
// sidecar file. Priority: --workdir flag, active sidecar, git remote default.
func resolveVariantsWorkspace(ctx context.Context, workdirFlag, projectDir string) string {
	if workdirFlag != "" {
		return workdirFlag
	}
	if active, err := sidecar.LoadActive(ctx); err == nil && active != nil && active.Workspace != "" {
		return active.Workspace
	}
	_, repo, err := gitremote.DetectOrgAndRepo(projectDir)
	if err == nil && repo != "" {
		return "./workspace/" + repo
	}
	return "./workspace"
}
