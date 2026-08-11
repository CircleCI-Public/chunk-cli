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
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
	"github.com/CircleCI-Public/chunk-cli/internal/variants"
)

func newValidateVariantsCmd() *cobra.Command {
	var name, orgID, image, identityFile, workdir string
	var parallel, timeout int

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

			rc, _ := config.Resolve("", "", insecureStorageFlag(cmd))
			client, err := ensureCircleCIClient(ctx, cmd, rc, streams, tui.PromptHidden)
			if err != nil {
				return err
			}

			if orgID == "" && cfg.OrgID != "" {
				orgID = cfg.OrgID
			}
			resolvedOrgID, err := resolveOrgID(orgID, workDir, orgPicker(ctx, client))
			if err != nil {
				return err
			}

			if image == "" && cfg.Validation != nil {
				image = cfg.Validation.SidecarImage
			}

			workspace, err := resolveVariantsWorkspace(ctx, workdir, workDir)
			if err != nil {
				return &userError{
					msg:        "Could not determine the remote workspace to sync into.",
					suggestion: "Run from a clone with an 'origin' remote, or pass --workdir.",
					err:        err,
				}
			}

			// Sweep before booting anything: a crashed earlier run can leave
			// sidecars the reaper will never collect, because variant sidecars
			// are deliberately absent from the active-sidecar file.
			statusFn := newStatusFunc(streams)
			if swept := variants.SweepOrphans(ctx, client, resolvedOrgID, statusFn); swept > 0 {
				statusFn(iostream.LevelInfo, fmt.Sprintf("swept %d orphaned variant sidecar(s) from a previous run", swept))
			}

			authSock := os.Getenv(config.EnvSSHAuthSock)
			results, err := variants.Run(ctx, client, vs, variants.Options{
				OrgID:        resolvedOrgID,
				Image:        image,
				IdentityFile: identityFile,
				AuthSock:     authSock,
				Workspace:    workspace,
				Parallel:     parallel,
				Commands:     variantCommands(cmds, workDir, timeout),
				StatusFn:     statusFn,
			})
			if err != nil {
				return &userError{msg: "Variants run failed.", err: err}
			}
			reportVariantSummary(results, statusFn)

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
	cmd.Flags().IntVar(&timeout, "timeout", validate.DefaultTimeout,
		"Per-command timeout in seconds, used when the command sets none (0 for no limit)")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&image, "image", "", "Snapshot image ID (default: validation.sidecarImage from config)")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Remote working directory")

	return cmd
}

// variantCommands prepares the configured commands for remote execution.
//
// Templates are expanded here, exactly as the ordinary remote path does in
// validate.RunRemote. Shipping a command unexpanded does not fail loudly: the
// literal {{CHANGED_PACKAGES}} reaches the sidecar's shell, which exits non-zero
// for a reason that has nothing to do with the code under test, and every variant
// in the run would be recorded as a caught mutant.
func variantCommands(cmds []config.Command, workDir string, defaultTimeout int) []variants.Command {
	out := make([]variants.Command, len(cmds))
	for i, c := range cmds {
		out[i] = variants.Command{
			Name:    c.Name,
			Run:     validate.ExpandCommand(workDir, c.Run),
			Timeout: commandTimeout(c.Timeout, defaultTimeout),
		}
	}
	return out
}

// commandTimeout picks the timeout for one command in seconds: the command's own
// setting when it has one, otherwise the run-wide default. A run-wide default of
// 0 means no limit, which is why this cannot simply take the larger of the two.
func commandTimeout(configured, fallback int) int {
	if configured > 0 {
		return configured
	}
	return fallback
}

// reportVariantSummary prints the run tally to stderr, alongside the JSON results
// on stdout.
//
// It calls out anything that makes a clean-looking result untrustworthy.
// Unassessed variants are gaps in the report rather than passes, and a run where
// nothing survived at all is more often one validate command failing the same way
// on every sidecar than a perfectly covered codebase — the two are
// indistinguishable in the per-variant JSON, so the difference has to be said out
// loud.
func reportVariantSummary(results []variants.Result, statusFn iostream.StatusFunc) {
	killed, unassessed := 0, 0
	for _, r := range results {
		switch {
		case r.Killed:
			killed++
		case r.Error != "":
			unassessed++
		}
	}

	statusFn(iostream.LevelDone, fmt.Sprintf("%d/%d variants killed", killed, len(results)))
	if unassessed > 0 {
		statusFn(iostream.LevelWarn, fmt.Sprintf(
			"%d variant(s) were not assessed and are neither killed nor survivors — see the 'error' field", unassessed))
	}
	if len(results) > 1 && killed == len(results) {
		statusFn(iostream.LevelWarn,
			"every variant was killed — check the failures look like test failures rather than a command that could not run on the snapshot")
	}
}

// resolveVariantsWorkspace derives the remote workspace path for variant workers.
// Every worker gets the same path, which is safe because each one drives its own
// sidecar: the path collides only within a machine, and no two variants share a
// machine.
//
// It defers to sidecar.ResolveWorkspace rather than reimplementing the priority
// order, so variants land where the rest of the CLI puts a workspace:
// <sidecarHome>/<repo>. That is not a cosmetic detail. `chunk sidecar env build`
// provisions dependencies in that directory before the snapshot is taken, so a
// sync into any other path gets a tree the snapshot never prepared, every
// command fails for environmental reasons, and every mutant reads as caught.
//
// The error is deliberate: SyncEphemeral has no shared state to fall back on, so
// an unresolvable workspace has to stop the run rather than pick a guess.
func resolveVariantsWorkspace(ctx context.Context, workdirFlag, projectDir string) (string, error) {
	if workdirFlag != "" {
		return workdirFlag, nil
	}
	// A missing origin remote is not fatal on its own — an active sidecar may
	// still name a workspace — so let ResolveWorkspace decide.
	_, repo, err := gitremote.DetectOrgAndRepo(projectDir)
	if err != nil {
		repo = ""
	}
	return sidecar.ResolveWorkspace(ctx, "", repo)
}
