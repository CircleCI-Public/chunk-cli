package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sandbox"
	"github.com/CircleCI-Public/chunk-cli/internal/secrets"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/usererr"
)

func newSandboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandboxes",
	}

	cmd.AddCommand(newSandboxListCmd())
	cmd.AddCommand(newSandboxCreateCmd())
	cmd.AddCommand(newSandboxExecCmd())
	cmd.AddCommand(newSandboxAddSSHKeyCmd())
	cmd.AddCommand(newSandboxSSHCmd())
	cmd.AddCommand(newSandboxSyncCmd())
	cmd.AddCommand(newSandboxEnvCmd())
	cmd.AddCommand(newSandboxBuildCmd())
	cmd.AddCommand(newSandboxUseCmd())
	cmd.AddCommand(newSandboxCurrentCmd())
	cmd.AddCommand(newSandboxForgetCmd())

	return cmd
}

// resolveSandboxID fills in sandboxID from the active sandbox file if it is empty.
func resolveSandboxID(sandboxID *string) error {
	if *sandboxID != "" {
		return nil
	}
	active, err := sandbox.LoadActive()
	if err != nil {
		return fmt.Errorf("load active sandbox: %w", err)
	}
	if active == nil {
		return fmt.Errorf("--sandbox-id is required (no active sandbox set; run 'chunk sandbox use <id>' or 'chunk sandbox create')")
	}
	*sandboxID = active.SandboxID
	return nil
}

// resolveOrgID returns orgID from the flag if set, otherwise falls back to
// the CIRCLECI_ORG_ID env var. Returns an error if neither is set.
func resolveOrgID(orgID string) (string, error) {
	if orgID != "" {
		return orgID, nil
	}
	if envID := os.Getenv("CIRCLECI_ORG_ID"); envID != "" {
		return envID, nil
	}
	return "", fmt.Errorf("--org-id is required: pass --org-id or set CIRCLECI_ORG_ID")
}

func newSandboxListCmd() *cobra.Command {
	var orgID string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sandboxes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			resolvedOrgID, err := resolveOrgID(orgID)
			if err != nil {
				return err
			}
			client, err := authprompt.EnsureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			sandboxes, err := sandbox.List(cmd.Context(), client, resolvedOrgID)
			if err != nil {
				return err
			}
			if len(sandboxes) == 0 {
				io.ErrPrintln(ui.Dim("No sandboxes found"))
				return nil
			}
			for _, s := range sandboxes {
				io.Printf("%s  %s\n", s.Name, s.ID)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")

	return cmd
}

const (
	defaultProvider = "e2b"
	providerEnvVar  = "CHUNK_SANDBOX_PROVIDER"
)

func newSandboxCreateCmd() *cobra.Command {
	var orgID, name, image string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a sandbox",
		Long: `Create a sandbox.

The sandbox backend defaults to e2b. Override with the CHUNK_SANDBOX_PROVIDER
environment variable (e.g. CHUNK_SANDBOX_PROVIDER=unikraft).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			resolvedOrgID, err := resolveOrgID(orgID)
			if err != nil {
				return err
			}
			client, err := authprompt.EnsureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			provider := os.Getenv(providerEnvVar)
			if provider == "" {
				provider = defaultProvider
			}
			sb, err := sandbox.Create(cmd.Context(), client, resolvedOrgID, name, provider, image)
			if err != nil {
				return err
			}
			io.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Created sandbox %s (%s)", sb.Name, sb.ID)))
			if err := sandbox.SaveActive(sandbox.ActiveSandbox{SandboxID: sb.ID, Name: sb.Name}); err != nil {
				io.ErrPrintf("warning: could not save active sandbox: %v\n", err)
			} else {
				io.ErrPrintf("Set %s as active sandbox\n", sb.ID)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&name, "name", "", "Sandbox name")
	cmd.Flags().StringVar(&image, "image", "", "E2B template ID or container image")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func newSandboxExecCmd() *cobra.Command {
	var sandboxID, command string
	var execArgs []string

	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Execute a command in a sandbox",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			if err := resolveSandboxID(&sandboxID); err != nil {
				return err
			}
			client, err := authprompt.EnsureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			// Combine --args flag values with positional args
			allArgs := make([]string, 0, len(execArgs)+len(args))
			allArgs = append(allArgs, execArgs...)
			allArgs = append(allArgs, args...)
			resp, err := sandbox.Exec(cmd.Context(), client, sandboxID, command, allArgs)
			if err != nil {
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

	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID (defaults to active sandbox)")
	cmd.Flags().StringVar(&command, "command", "", "Command to execute")
	cmd.Flags().StringArrayVar(&execArgs, "args", nil, "Command arguments")
	_ = cmd.MarkFlagRequired("command")

	return cmd
}

func newSandboxAddSSHKeyCmd() *cobra.Command {
	var sandboxID, publicKey, publicKeyFile string

	cmd := &cobra.Command{
		Use:   "add-ssh-key",
		Short: "Add an SSH public key to a sandbox",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			if err := resolveSandboxID(&sandboxID); err != nil {
				return err
			}
			client, err := authprompt.EnsureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			resp, err := sandbox.AddSSHKey(cmd.Context(), client, sandboxID, publicKey, publicKeyFile)
			if err != nil {
				return err
			}
			io.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("SSH key added. Sandbox URL: %s", resp.URL)))
			return nil
		},
	}

	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID (defaults to active sandbox)")
	cmd.Flags().StringVar(&publicKey, "public-key", "", "SSH public key string")
	cmd.Flags().StringVar(&publicKeyFile, "public-key-file", "", "Path to SSH public key file")

	return cmd
}

func newSandboxSSHCmd() *cobra.Command {
	var sandboxID, identityFile, envFile string
	var envVarsFlag []string

	cmd := &cobra.Command{
		Use:   "ssh [flags] [-- command...]",
		Short: "SSH into a sandbox",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			if err := resolveSandboxID(&sandboxID); err != nil {
				return err
			}
			authSock := os.Getenv("SSH_AUTH_SOCK")
			client, err := authprompt.EnsureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			flagVars, err := sandbox.ParseEnvPairs(envVarsFlag)
			if err != nil {
				return usererr.New(fmt.Sprintf("invalid --env value: %s", err), err)
			}
			var envVars map[string]string
			if envFile != "" {
				path := envFile
				if !filepath.IsAbs(path) {
					cwd, err := os.Getwd()
					if err != nil {
						return fmt.Errorf("get working directory: %w", err)
					}
					path = filepath.Join(cwd, path)
				}
				fileVars, err := sandbox.LoadEnvFileAt(path)
				if err != nil {
					return usererr.New(fmt.Sprintf("load %s: %s", envFile, err), err)
				}
				envVars = sandbox.MergeEnv(fileVars, flagVars)
			} else {
				envVars = flagVars
			}
			resolved, err := secrets.ResolveAll(cmd.Context(), envVars, nil)
			if err != nil {
				return usererr.New(fmt.Sprintf("resolve secrets: %s", err), err)
			}
			envVars = resolved
			return sandbox.SSH(cmd.Context(), client, sandboxID, identityFile, authSock, args, envVars, io)
		},
	}

	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID (defaults to active sandbox)")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file")
	cmd.Flags().StringArrayVarP(&envVarsFlag, "env", "e", nil, "KEY=VALUE pairs to set in the remote session (repeatable)")
	cmd.Flags().StringVar(&envFile, "env-file", "", "Env file to load (default .env.local when flag is present)")
	cmd.Flags().Lookup("env-file").NoOptDefVal = ".env.local"

	return cmd
}

func newSandboxSyncCmd() *cobra.Command {
	var sandboxID, identityFile, workdir string

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync files to a sandbox",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			if err := resolveSandboxID(&sandboxID); err != nil {
				return err
			}
			authSock := os.Getenv("SSH_AUTH_SOCK")
			client, err := authprompt.EnsureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
			if err != nil {
				return err
			}
			return sandbox.Sync(cmd.Context(), client, sandboxID, identityFile, authSock, workdir, io)
		},
	}

	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID (defaults to active sandbox)")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Destination path on sandbox (auto-detected as /workspace/<repo> when omitted)")

	return cmd
}

func newSandboxUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <sandbox-id>",
		Short: "Set the active sandbox for this project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			if err := sandbox.SaveActive(sandbox.ActiveSandbox{SandboxID: args[0]}); err != nil {
				return err
			}
			io.ErrPrintf("Set %s as active sandbox\n", args[0])
			return nil
		},
	}
}

func newSandboxCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the active sandbox",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			active, err := sandbox.LoadActive()
			if err != nil {
				return err
			}
			if active == nil {
				io.ErrPrintln("No active sandbox")
				return nil
			}
			if active.Name != "" {
				io.Printf("%s  %s\n", active.Name, active.SandboxID)
			} else {
				io.Printf("%s\n", active.SandboxID)
			}
			return nil
		},
	}
}

func newSandboxForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget",
		Short: "Clear the active sandbox",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			if err := sandbox.ClearActive(); err != nil {
				return err
			}
			io.ErrPrintln("Active sandbox cleared")
			return nil
		},
	}
}

var validDockerTag = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/\-]*(:[a-zA-Z0-9._\-]+)?$`)

func newSandboxEnvCmd() *cobra.Command {
	var dir string

	cmd := &cobra.Command{
		Use:   "env",
		Short: "Detect tech stack and print environment spec as JSON",
		Long: `Analyse the repository at --dir, detect its tech stack, and print
a JSON environment spec to stdout. Pipe this into 'chunk sandbox build' to
generate a Dockerfile and build a test image.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			if _, err := os.Stat(dir); err != nil {
				return fmt.Errorf("directory %q not found: %w", dir, err)
			}
			io.ErrPrintf("Detecting environment in %s...\n", dir)

			env, err := envbuilder.DetectEnvironment(cmd.Context(), dir)
			if err != nil {
				return fmt.Errorf("detect environment: %w", err)
			}

			out, err := json.MarshalIndent(env, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal environment: %w", err)
			}
			io.Printf("%s\n", out)
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", ".", "Directory to detect environment in")

	return cmd
}

func newSandboxBuildCmd() *cobra.Command {
	var dir, tag string

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Generate a Dockerfile from an environment spec and build a test image",
		Long: `Read a JSON environment spec from stdin (produced by 'chunk sandbox env'),
write Dockerfile.test to --dir, and build a Docker test image from it.

Example:
  chunk sandbox env --dir . | chunk sandbox build --dir .`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tag != "" && !validDockerTag.MatchString(tag) {
				return fmt.Errorf("invalid docker tag %q", tag)
			}

			streams := iostream.FromCmd(cmd)

			// Guard against interactive use: if stdin is a terminal (not a pipe),
			// fail fast with a helpful message rather than blocking silently.
			// Check cmd.InOrStdin() so injected readers (e.g. in tests) are not blocked.
			if f, ok := cmd.InOrStdin().(*os.File); ok {
				if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
					return fmt.Errorf("no input on stdin — pipe a JSON env spec from 'chunk sandbox env'")
				}
			}

			raw, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("read environment spec: %w", err)
			}
			var env envbuilder.Environment
			if err := json.Unmarshal(raw, &env); err != nil {
				return fmt.Errorf("parse environment spec: %w", err)
			}

			dockerfilePath, err := envbuilder.WriteDockerfile(dir, &env)
			if err != nil {
				return fmt.Errorf("write dockerfile: %w", err)
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
				return fmt.Errorf("docker build: %w", err)
			}

			streams.ErrPrintf("%s\n", ui.Success("Docker image built successfully"))
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", ".", "Directory to write Dockerfile.test and build from")
	cmd.Flags().StringVar(&tag, "tag", "", "Image tag (e.g. myapp:latest)")

	return cmd
}
