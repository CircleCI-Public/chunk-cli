package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
)

func newPreviewCmd() *cobra.Command {
	var dir, sidecarID, orgID, name, identityFile, envFile string
	var envVarsFlag []string
	var port int
	var command string
	var newSidecar bool

	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Sync the current directory to a sidecar and preview a running app",
		Long: `Sync the current directory to a sidecar, start an app on it, and print a
URL to preview it in a browser.

By default, reuses the active sidecar if one is set (creating one first if
not). Pass --new to always create a fresh sidecar instead of reusing the
active one, e.g. to isolate this preview from other work.

If no active sidecar is set, pass --org-id and --name to create one first.

Example:
  chunk preview --port 3000 --command "npm run dev"
  chunk preview --port 3000 --command "npm run dev" --name my-sidecar --org-id <org-id>
  chunk preview --port 3000 --command "npm run dev" --new --org-id <org-id>`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			streams := iostream.FromCmd(cmd)
			status := newStatusFunc(streams)
			authSock := os.Getenv("SSH_AUTH_SOCK")

			insecureStorage := insecureStorageFlag(cmd)
			rc, _ := config.Resolve("", "", insecureStorage)
			client, err := ensureCircleCIClient(cmd.Context(), cmd, rc, streams, tui.PromptHidden)
			if err != nil {
				return err
			}

			if sidecarID == "" {
				var resolveErr error
				sidecarID, resolveErr = sidecarSetupResolveSidecar(cmd.Context(), client, orgID, name, dir, newSidecar, status, streams)
				if resolveErr != nil {
					return resolveErr
				}
			} else if newSidecar {
				return &userError{
					msg:        "--sidecar-id and --new cannot be used together.",
					suggestion: "Pass either --sidecar-id to target an existing sidecar or --new to create one.",
				}
			}

			if err := sidecarSetupEnsureSSHKey(identityFile, status); err != nil {
				return err
			}

			if err := sidecarSetupSync(cmd.Context(), client, sidecarID, identityFile, authSock, status); err != nil {
				return err
			}

			envVars, err := resolveEnvVars(cmd.Context(), dir, envFile, envVarsFlag)
			if err != nil {
				return err
			}

			session, err := sidecar.OpenSession(cmd.Context(), client, sidecarID, identityFile, authSock)
			if err != nil {
				if sessErr := sshSessionError(err); sessErr != nil {
					return sessErr
				}
				return err
			}

			var workspace string
			if active, loadErr := sidecar.LoadActive(cmd.Context()); loadErr == nil && active != nil {
				workspace = active.Workspace
			}

			status(iostream.LevelStep, fmt.Sprintf("Starting %q on the sidecar...", command))
			if err := sidecar.StartPreviewServer(cmd.Context(), session, workspace, command, port, envVars, status); err != nil {
				if sessErr := sshSessionError(err); sessErr != nil {
					return sessErr
				}
				return &userError{
					msg:        fmt.Sprintf("App did not start listening on port %d.", port),
					suggestion: fmt.Sprintf("Check the logs: chunk sidecar ssh -- cat %s", sidecar.PreviewLogPath),
					err:        err,
				}
			}

			previewURL, err := sidecar.BuildPreviewURL(session.URL, port)
			if err != nil {
				return &userError{msg: "Could not build a preview URL for this sidecar.", err: err}
			}

			status(iostream.LevelDone, "App is running")
			streams.Println(previewURL)
			streams.ErrPrintf("\nLogs: chunk sidecar ssh -- tail -f %s\n", sidecar.PreviewLogPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", ".", "Directory to sync")
	cmd.Flags().StringVar(&sidecarID, "sidecar-id", "", "Sidecar ID (defaults to active sidecar)")
	cmd.Flags().BoolVar(&newSidecar, "new", false, "Always create a new sidecar instead of reusing the active one")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID (used when creating a new sidecar)")
	cmd.Flags().StringVar(&name, "name", "", "Sidecar name (used when creating a new sidecar)")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file")
	cmd.Flags().StringArrayVarP(&envVarsFlag, "env", "e", nil, "KEY=VALUE pairs to set in remote sidecar session (repeatable)")
	cmd.Flags().StringVar(&envFile, "env-file", defaultEnvFile, "Env file to load (default: .env.local; pass a path to override)")
	cmd.Flags().IntVar(&port, "port", 0, "Port the app listens on (required)")
	cmd.Flags().StringVar(&command, "command", "", "Command to start the app on the sidecar (required)")
	_ = cmd.MarkFlagRequired("port")
	_ = cmd.MarkFlagRequired("command")

	return cmd
}
