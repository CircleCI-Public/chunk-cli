package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/secrets"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newSidecarCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sidecar",
		Short: "Manage sidecars",
	}

	cmd.AddCommand(newSidecarListCmd())
	cmd.AddCommand(newSidecarCreateCmd())
	cmd.AddCommand(newSidecarExecCmd())
	cmd.AddCommand(newSidecarAddSSHKeyCmd())
	cmd.AddCommand(newSidecarSSHCmd())
	cmd.AddCommand(newSidecarSyncCmd())
	cmd.AddCommand(newSidecarEnvCmd())
	cmd.AddCommand(newSidecarBuildCmd())
	cmd.AddCommand(newSidecarUseCmd())
	cmd.AddCommand(newSidecarCurrentCmd())
	cmd.AddCommand(newSidecarForgetCmd())
	cmd.AddCommand(newSidecarSnapshotCmd())

	return cmd
}

// resolveSidecarID fills in sidecarID from the active sidecar file if it is empty.
func resolveSidecarID(sidecarID *string) error {
	if *sidecarID != "" {
		return nil
	}
	active, err := sidecar.LoadActive()
	if err != nil {
		return &userError{msg: "Could not load the active sidecar.", suggestion: configFilePermHint, err: err}
	}
	if active == nil {
		return &userError{
			msg:        "No active sidecar is set.",
			suggestion: "Pass --sidecar-id, or run 'chunk sidecar use <id>' or 'chunk sidecar create'.",
			errMsg:     "no active sidecar and --sidecar-id not provided",
		}
	}
	*sidecarID = active.SidecarID
	return nil
}

// resolveOrgID returns orgID from the flag, the CIRCLECI_ORG_ID env var,
// or by calling pickOrg as a last resort (e.g. to present a TUI picker).
func resolveOrgID(orgID string, pickOrg func() (string, error)) (string, error) {
	if orgID != "" {
		return orgID, nil
	}
	if envID := os.Getenv(config.EnvCircleCIOrgID); envID != "" {
		return envID, nil
	}
	return pickOrg()
}

func orgPicker(ctx context.Context, client *circleci.Client) func() (string, error) {
	return func() (string, error) {
		collabs, err := client.ListCollaborations(ctx)
		if err != nil {
			if err := notAuthorized("list organizations", err); err != nil {
				return "", err
			}
			return "", &userError{
				msg:        "Could not list organizations.",
				suggestion: "Pass --org-id or check your network connection.",
				err:        err,
			}
		}
		if len(collabs) == 0 {
			return "", &userError{
				msg:        "No organizations found.",
				suggestion: "Pass --org-id or join an organization in CircleCI.",
				err:        fmt.Errorf("no organizations found for current user"),
			}
		}
		labels := make([]string, len(collabs))
		for i, c := range collabs {
			labels[i] = fmt.Sprintf("%s/%s", c.VcsType, c.Name)
		}
		idx, err := tui.SelectFromList("Select an organization:", labels)
		if err != nil {
			return "", &userError{msg: "Could not select an organization.", suggestion: "Pass --org-id instead.", err: err}
		}
		return collabs[idx].ID, nil
	}
}

func newSidecarListCmd() *cobra.Command {
	var orgID string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sidecars",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			client, err := ensureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			resolvedOrgID, err := resolveOrgID(orgID, orgPicker(cmd.Context(), client))
			if err != nil {
				return err
			}
			sidecars, err := sidecar.List(cmd.Context(), client, resolvedOrgID)
			if err != nil {
				if err := notAuthorized("list sidecars", err); err != nil {
					return err
				}
				return &userError{
					msg:        "Could not list sidecars.",
					suggestion: "Check your network connection and try again.",
					err:        err,
				}
			}
			if len(sidecars) == 0 {
				io.ErrPrintln(ui.Dim("No sidecars found"))
				return nil
			}
			for _, s := range sidecars {
				io.Printf("%s  %s\n", s.Name, s.ID)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")

	return cmd
}

const defaultProvider = "e2b"

func newSidecarCreateCmd() *cobra.Command {
	var orgID, name, image string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a sidecar",
		Long: "Create a sidecar.\n\nThe sidecar backend defaults to e2b. Override with the " + config.EnvSidecarProvider +
			"\nenvironment variable (e.g. " + config.EnvSidecarProvider + "=unikraft).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			client, err := ensureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			resolvedOrgID, err := resolveOrgID(orgID, orgPicker(cmd.Context(), client))
			if err != nil {
				return err
			}
			provider := os.Getenv(config.EnvSidecarProvider)
			if provider == "" {
				provider = defaultProvider
			}
			sb, err := sidecar.Create(cmd.Context(), client, resolvedOrgID, name, provider, image)
			if err != nil {
				if err := notAuthorized("create sidecars", err); err != nil {
					return err
				}
				return &userError{
					msg:        "Could not create the sidecar.",
					suggestion: "Check your network connection and try again.",
					err:        err,
				}
			}
			io.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Created sidecar %s (%s)", sb.Name, sb.ID)))
			if err := sidecar.SaveActive(sidecar.ActiveSidecar{SidecarID: sb.ID, Name: sb.Name}); err != nil {
				io.ErrPrintf("warning: could not save active sidecar: %v\n", err)
			} else {
				io.ErrPrintf("Set %s as active sidecar\n", sb.ID)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&name, "name", "", "Sidecar name")
	cmd.Flags().StringVar(&image, "image", "", "E2B template ID or container image")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func newSidecarExecCmd() *cobra.Command {
	var sidecarID, command string
	var execArgs []string

	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Execute a command in a sidecar",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			if err := resolveSidecarID(&sidecarID); err != nil {
				return err
			}
			client, err := ensureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			// Combine --args flag values with positional args
			allArgs := make([]string, 0, len(execArgs)+len(args))
			allArgs = append(allArgs, execArgs...)
			allArgs = append(allArgs, args...)
			resp, err := sidecar.Exec(cmd.Context(), client, sidecarID, command, allArgs)
			if err != nil {
				if err := notAuthorized("execute commands", err); err != nil {
					return err
				}
				return err
			}
			if resp.Stdout != "" {
				_, _ = fmt.Fprint(io.Out, resp.Stdout)
			}
			if resp.Stderr != "" {
				_, _ = fmt.Fprint(io.Err, resp.Stderr)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sidecarID, "sidecar-id", "", "Sidecar ID (defaults to active sidecar)")
	cmd.Flags().StringVar(&command, "command", "", "Command to execute")
	cmd.Flags().StringArrayVar(&execArgs, "args", nil, "Command arguments")
	_ = cmd.MarkFlagRequired("command")

	return cmd
}

func newSidecarAddSSHKeyCmd() *cobra.Command {
	var sidecarID, publicKey, publicKeyFile string

	cmd := &cobra.Command{
		Use:   "add-ssh-key",
		Short: "Add an SSH public key to a sidecar",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			if err := resolveSidecarID(&sidecarID); err != nil {
				return err
			}
			client, err := ensureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			resp, err := sidecar.AddSSHKey(cmd.Context(), client, sidecarID, publicKey, publicKeyFile)
			if err != nil {
				if err := notAuthorized("add SSH keys", err); err != nil {
					return err
				}
				switch {
				case errors.Is(err, sidecar.ErrPrivateKeyProvided):
					return &userError{
						msg:        "The provided key is a private key.",
						suggestion: "Provide a public key instead.",
						err:        err,
					}
				case errors.Is(err, sidecar.ErrMutuallyExclusiveKeys):
					return &userError{
						msg: "--public-key and --public-key-file are mutually exclusive.",
						err: err,
					}
				case errors.Is(err, sidecar.ErrPublicKeyRequired):
					return &userError{
						msg:        "A public key is required.",
						suggestion: "Pass --public-key or --public-key-file.",
						err:        err,
					}
				}
				return err
			}
			io.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("SSH key added. Sidecar URL: %s", resp.URL)))
			return nil
		},
	}

	cmd.Flags().StringVar(&sidecarID, "sidecar-id", "", "Sidecar ID (defaults to active sidecar)")
	cmd.Flags().StringVar(&publicKey, "public-key", "", "SSH public key string")
	cmd.Flags().StringVar(&publicKeyFile, "public-key-file", "", "Path to SSH public key file")

	return cmd
}

func newSidecarSSHCmd() *cobra.Command {
	var sidecarID, identityFile, envFile string
	var envVarsFlag []string

	cmd := &cobra.Command{
		Use:   "ssh [flags] [-- command...]",
		Short: "SSH into a sidecar",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			if err := resolveSidecarID(&sidecarID); err != nil {
				return err
			}
			authSock := os.Getenv(config.EnvSSHAuthSock)
			client, err := ensureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			flagVars, err := sidecar.ParseEnvPairs(envVarsFlag)
			if err != nil {
				return &userError{msg: fmt.Sprintf("invalid --env value: %s", err), err: err}
			}
			var envVars map[string]string
			if envFile != "" {
				path := envFile
				if !filepath.IsAbs(path) {
					cwd, err := os.Getwd()
					if err != nil {
						return &userError{msg: "Could not determine the current directory.", err: err}
					}
					path = filepath.Join(cwd, path)
				}
				fileVars, err := sidecar.LoadEnvFileAt(path)
				if err != nil {
					return &userError{msg: fmt.Sprintf("load %s: %s", envFile, err), err: err}
				}
				envVars = sidecar.MergeEnv(fileVars, flagVars)
			} else {
				envVars = flagVars
			}
			resolved, err := secrets.ResolveAll(cmd.Context(), envVars, nil)
			if err != nil {
				return &userError{msg: fmt.Sprintf("resolve secrets: %s", err), err: err}
			}
			envVars = resolved
			err = sidecar.SSH(cmd.Context(), client, sidecarID, identityFile, authSock, args, envVars, io)
			if err != nil {
				if err := sshSessionError(err); err != nil {
					return err
				}
				if err := notAuthorized("connect via SSH", err); err != nil {
					return err
				}
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sidecarID, "sidecar-id", "", "Sidecar ID (defaults to active sidecar)")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file")
	cmd.Flags().StringArrayVarP(&envVarsFlag, "env", "e", nil, "KEY=VALUE pairs to set in the remote session (repeatable)")
	cmd.Flags().StringVar(&envFile, "env-file", "", "Env file to load (default .env.local when flag is present)")
	cmd.Flags().Lookup("env-file").NoOptDefVal = ".env.local"

	return cmd
}

func newSidecarSyncCmd() *cobra.Command {
	var sidecarID, identityFile, workdir string

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync files to a sidecar",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			if err := resolveSidecarID(&sidecarID); err != nil {
				return err
			}
			authSock := os.Getenv(config.EnvSSHAuthSock)
			client, err := ensureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			err = sidecar.Sync(cmd.Context(), client, sidecarID, identityFile, authSock, workdir, newStatusFunc(io))
			if err != nil {
				if _, ok := errors.AsType[*sidecar.RemoteBaseError](err); ok {
					return &userError{
						msg:        "Could not resolve remote base.",
						suggestion: "Push your branch or ensure the repository has a remote configured.",
						err:        err,
					}
				}
				if err := sshSessionError(err); err != nil {
					return err
				}
				if err := notAuthorized("sync files", err); err != nil {
					return err
				}
				return &userError{
					msg: "The sync operation failed.",
					err: err,
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sidecarID, "sidecar-id", "", "Sidecar ID (defaults to active sidecar)")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Destination path on sidecar (auto-detected as /workspace/<repo> when omitted)")

	return cmd
}

func newSidecarUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <sidecar-id>",
		Short: "Set the active sidecar for this project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			if err := sidecar.SaveActive(sidecar.ActiveSidecar{SidecarID: args[0]}); err != nil {
				return &userError{msg: "Could not save the active sidecar.", suggestion: configFilePermHint, err: err}
			}
			io.ErrPrintf("Set %s as active sidecar\n", args[0])
			return nil
		},
	}
}

func newSidecarCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the active sidecar",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			active, err := sidecar.LoadActive()
			if err != nil {
				return &userError{msg: "Could not load the active sidecar.", suggestion: configFilePermHint, err: err}
			}
			if active == nil {
				io.ErrPrintln("No active sidecar")
				return nil
			}
			if active.Name != "" {
				io.Printf("%s  %s\n", active.Name, active.SidecarID)
			} else {
				io.Printf("%s\n", active.SidecarID)
			}
			return nil
		},
	}
}

func newSidecarForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget",
		Short: "Clear the active sidecar",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			if err := sidecar.ClearActive(); err != nil {
				return &userError{msg: "Could not clear the active sidecar.", suggestion: configFilePermHint, err: err}
			}
			io.ErrPrintln("Active sidecar cleared")
			return nil
		},
	}
}

var validDockerTag = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/\-]*(:[a-zA-Z0-9._\-]+)?$`)

func newSidecarEnvCmd() *cobra.Command {
	var dir string

	cmd := &cobra.Command{
		Use:   "env",
		Short: "Detect tech stack and print environment spec as JSON",
		Long: `Analyse the repository at --dir, detect its tech stack, and print
a JSON environment spec to stdout. Pipe this into 'chunk sidecar build' to
generate a Dockerfile and build a test image.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			if _, err := os.Stat(dir); err != nil {
				return &userError{
					msg:        fmt.Sprintf("Directory %q not found.", dir),
					suggestion: "Check the --dir path and try again.",
					err:        err,
				}
			}
			io.ErrPrintf("Detecting environment in %s...\n", dir)

			env, err := envbuilder.DetectEnvironment(cmd.Context(), dir)
			if err != nil {
				return &userError{
					msg:        "Could not detect the environment.",
					suggestion: "Check the directory contains a supported project.",
					err:        err,
				}
			}

			out, err := json.MarshalIndent(env, "", "  ")
			if err != nil {
				return &userError{msg: "Could not encode the environment spec.", err: err}
			}
			io.Printf("%s\n", out)
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", ".", "Directory to detect environment in")

	return cmd
}

func newSidecarBuildCmd() *cobra.Command {
	var dir, tag string

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Generate a Dockerfile from an environment spec and build a test image",
		Long: `Read a JSON environment spec from stdin (produced by 'chunk sidecar env'),
write Dockerfile.test to --dir, and build a Docker test image from it.

Example:
  chunk sidecar env --dir . | chunk sidecar build --dir .`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tag != "" && !validDockerTag.MatchString(tag) {
				return &userError{
					msg:        fmt.Sprintf("Invalid image tag %q.", tag),
					suggestion: "Use a tag like 'myapp:latest'.",
					errMsg:     fmt.Sprintf("invalid docker tag %q", tag),
				}
			}

			streams := iostream.FromCmd(cmd)

			// Guard against interactive use: if stdin is a terminal (not a pipe),
			// fail fast with a helpful message rather than blocking silently.
			// Check cmd.InOrStdin() so injected readers (e.g. in tests) are not blocked.
			if f, ok := cmd.InOrStdin().(*os.File); ok {
				if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
					return &userError{
						msg:        "No input on stdin.",
						suggestion: "Pipe a JSON env spec, for example: chunk sidecar env | chunk sidecar build",
						errMsg:     "no input on stdin",
					}
				}
			}

			raw, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return &userError{msg: "Could not read the environment spec from stdin.", err: err}
			}
			var env envbuilder.Environment
			if err := json.Unmarshal(raw, &env); err != nil {
				return &userError{
					msg:        "Invalid environment spec.",
					suggestion: "Pipe the output of 'chunk sidecar env' into this command.",
					err:        err,
				}
			}

			dockerfilePath, err := envbuilder.WriteDockerfile(dir, &env)
			if err != nil {
				return &userError{
					msg:        "Could not write the Dockerfile.",
					suggestion: "Check directory permissions and try again.",
					err:        err,
				}
			}
			streams.ErrPrintf("Wrote %s\n", dockerfilePath)

			streams.ErrPrintf("Building Docker image in %s...\n", dir)

			args := []string{"build", "-f", "Dockerfile.test"}
			if tag != "" {
				args = append(args, "-t", tag)
			}
			args = append(args, ".")

			dockerCmd := exec.CommandContext(cmd.Context(), "docker", args...)
			dockerCmd.Dir = dir
			dockerCmd.Stdout = streams.Out
			dockerCmd.Stderr = streams.Err
			if err := dockerCmd.Run(); err != nil {
				return &userError{msg: "Docker build failed.", suggestion: "Check the build output above for details.", err: err}
			}

			streams.ErrPrintf("%s\n", ui.Success("Docker image built successfully"))
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", ".", "Directory to write Dockerfile.test and build from")
	cmd.Flags().StringVar(&tag, "tag", "", "Image tag (e.g. myapp:latest)")

	return cmd
}

func newSidecarSnapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage sidecar snapshots",
	}
	cmd.AddCommand(newSidecarSnapshotCreateCmd())
	cmd.AddCommand(newSidecarSnapshotGetCmd())
	return cmd
}

func newSidecarSnapshotCreateCmd() *cobra.Command {
	var sidecarID, name string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a snapshot of a sidecar",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			if err := resolveSidecarID(&sidecarID); err != nil {
				return err
			}
			client, err := ensureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			snap, err := client.CreateSnapshot(cmd.Context(), sidecarID, name)
			if err != nil {
				return err
			}
			io.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Created snapshot %s", snap.ID)))
			return nil
		},
	}

	cmd.Flags().StringVar(&sidecarID, "sidecar-id", "", "Sidecar ID (defaults to active sidecar)")
	cmd.Flags().StringVar(&name, "name", "", "Snapshot name")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func newSidecarSnapshotGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <snapshot-id>",
		Short: "Get a snapshot by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			client, err := ensureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			snap, err := client.GetSnapshot(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if snap.Name != "" {
				io.Printf("%s  %s\n", snap.Name, snap.ID)
			} else {
				io.Printf("%s\n", snap.ID)
			}
			return nil
		},
	}

	return cmd
}
