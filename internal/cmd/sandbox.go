package cmd

import (
	"context"
	"encoding/json"
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
	"github.com/CircleCI-Public/chunk-cli/internal/sandbox"
	"github.com/CircleCI-Public/chunk-cli/internal/secrets"
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

// resolveProvider returns the CHUNK_SANDBOX_PROVIDER env var if set,
// otherwise the default ("e2b").
func resolveProvider() string {
	if v := os.Getenv(providerEnvVar); v != "" {
		return v
	}
	return defaultProvider
}

// resolveOrgID returns orgID from the flag if set, otherwise falls back to
// circleci.orgId in .chunk/config.json. Returns an error if neither is set.
func resolveOrgID(orgID string) (string, error) {
	if orgID != "" {
		return orgID, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	if projCfg, loadErr := config.LoadProjectConfig(cwd); loadErr == nil && projCfg.CircleCI != nil && projCfg.CircleCI.OrgID != "" {
		return projCfg.CircleCI.OrgID, nil
	}
	return "", fmt.Errorf("--org-id is required: pass --org-id or run 'chunk init' to store it in .chunk/config.json")
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
			client, err := circleci.NewClient()
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
	var quiet bool

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
			client, err := circleci.NewClient()
			if err != nil {
				return err
			}
			sb, err := sandbox.Create(cmd.Context(), client, resolvedOrgID, name, resolveProvider(), image)
			if err != nil {
				return err
			}
			if err := sandbox.SaveActive(sandbox.ActiveSandbox{SandboxID: sb.ID, Name: sb.Name}); err != nil {
				io.ErrPrintf("warning: could not save active sandbox: %v\n", err)
			}
			if quiet {
				io.Printf("%s\n", sb.ID)
			} else {
				io.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Created sandbox %s (%s)", sb.Name, sb.ID)))
				io.ErrPrintf("Set %s as active sandbox\n", sb.ID)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&name, "name", "", "Sandbox name")
	cmd.Flags().StringVar(&image, "image", "", "E2B template ID or container image")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Print only the sandbox ID")
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
			client, err := circleci.NewClient()
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
			client, err := circleci.NewClient()
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
			if err := resolveSandboxID(&sandboxID); err != nil {
				return err
			}
			authSock := os.Getenv("SSH_AUTH_SOCK")
			client, err := circleci.NewClient()
			if err != nil {
				return err
			}
			envVars, err := resolveEnvVars(cmd.Context(), envVarsFlag, envFile)
			if err != nil {
				return err
			}
			io := iostream.FromCmd(cmd)
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
			client, err := circleci.NewClient()
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
a JSON environment spec to stdout. The result is also saved to
.chunk/config.json for use by 'chunk sandbox env setup'.

Pipe the output into 'chunk sandbox build' to generate a Dockerfile and
build a test image.`,
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

			// Persist to .chunk/config.json.
			envJSON, err := json.Marshal(env)
			if err != nil {
				return fmt.Errorf("marshal environment: %w", err)
			}
			cfg, _ := config.LoadProjectConfig(dir)
			if cfg == nil {
				cfg = &config.ProjectConfig{}
			}
			cfg.Environment = json.RawMessage(envJSON)
			if err := config.SaveProjectConfig(dir, cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			io.ErrPrintf("Saved environment to .chunk/config.json\n")

			out, err := json.MarshalIndent(env, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal environment: %w", err)
			}
			io.Printf("%s\n", out)
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", ".", "Directory to detect environment in")

	cmd.AddCommand(newSandboxEnvSetupCmd())

	return cmd
}

func newSandboxBuildCmd() *cobra.Command {
	var dir, tag string

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Generate a Dockerfile from an environment spec and build a test image",
		Long: `Read a JSON environment spec from stdin (produced by 'chunk sandbox env'),
write Dockerfile.test to --dir, and build a Docker test image from it.

When CHUNK_BUILD_COMMANDS is set, the ordered setup steps from the environment
spec are run directly as shell commands instead of building a Docker image.

When CHUNK_CAPTURE_COMMANDS is set to a file path, the steps are written as
JSON to that file without executing anything. Set to "1" to write to
build-steps.json in --dir.

Example:
  chunk sandbox env --dir . | chunk sandbox build --dir .`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tag != "" && !validDockerTag.MatchString(tag) {
				return fmt.Errorf("invalid docker tag %q", tag)
			}

			streams := iostream.FromCmd(cmd)

			// Guard against interactive use: if stdin is a terminal (not a pipe),
			// fail fast with a helpful message rather than blocking silently.
			if stdinIsTerminal(cmd) {
				return fmt.Errorf("no input on stdin — pipe a JSON env spec from 'chunk sandbox env'")
			}

			raw, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("read environment spec: %w", err)
			}
			var env envbuilder.Environment
			if err := json.Unmarshal(raw, &env); err != nil {
				return fmt.Errorf("parse environment spec: %w", err)
			}

			if dest := os.Getenv("CHUNK_CAPTURE_COMMANDS"); dest != "" {
				if dest == "1" {
					dest = filepath.Join(dir, "build-steps.json")
				}
				return captureCommands(dest, &env, streams)
			}
			if os.Getenv("CHUNK_BUILD_COMMANDS") != "" {
				return runBuildSteps(cmd, dir, &env, streams)
			}
			return runDockerBuild(cmd, dir, tag, &env, streams)
		},
	}

	cmd.Flags().StringVar(&dir, "dir", ".", "Directory to write Dockerfile.test and build from")
	cmd.Flags().StringVar(&tag, "tag", "", "Image tag (e.g. myapp:latest)")

	return cmd
}

// runDockerBuild writes Dockerfile.test and runs docker build.
func runDockerBuild(cmd *cobra.Command, dir, tag string, env *envbuilder.Environment, streams iostream.Streams) error {
	dockerfilePath, err := envbuilder.WriteDockerfile(dir, env)
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
}

// runBuildSteps runs each environment setup step directly as a shell command.
func runBuildSteps(cmd *cobra.Command, dir string, env *envbuilder.Environment, streams iostream.Streams) error {
	if len(env.Steps) == 0 {
		streams.ErrPrintln(ui.Dim("No build steps to run."))
		return nil
	}
	for _, step := range env.Steps {
		streams.ErrPrintf("%s %s\n", ui.Dim("Running:"), ui.Gray(step.Command))
		c := exec.CommandContext(cmd.Context(), "bash", "-l", "-c", step.Command)
		c.Dir = dir
		c.Stdout = streams.Out
		c.Stderr = streams.Err
		if err := c.Run(); err != nil {
			return fmt.Errorf("step %q: %w", step.Name, err)
		}
	}
	streams.ErrPrintf("%s\n", ui.Success("Build steps completed successfully"))
	return nil
}

// captureCommands writes the environment steps to a JSON file without executing them.
func captureCommands(dest string, env *envbuilder.Environment, streams iostream.Streams) error {
	out, err := json.MarshalIndent(env.Steps, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal steps: %w", err)
	}
	if err := os.WriteFile(dest, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	streams.ErrPrintf("Wrote %s\n", dest)
	return nil
}

// resolveEnvVars parses env flags, optionally loads an env file, merges them,
// and resolves any secret references (e.g. op:// URIs).
func resolveEnvVars(ctx context.Context, envVarsFlag []string, envFile string) (map[string]string, error) {
	flagVars, err := sandbox.ParseEnvPairs(envVarsFlag)
	if err != nil {
		return nil, usererr.New(fmt.Sprintf("invalid --env value: %s", err), err)
	}
	var envVars map[string]string
	if envFile != "" {
		path := envFile
		if !filepath.IsAbs(path) {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("get working directory: %w", err)
			}
			path = filepath.Join(cwd, path)
		}
		fileVars, err := sandbox.LoadEnvFileAt(path)
		if err != nil {
			return nil, usererr.New(fmt.Sprintf("load %s: %s", envFile, err), err)
		}
		envVars = sandbox.MergeEnv(fileVars, flagVars)
	} else {
		envVars = flagVars
	}
	resolved, err := secrets.ResolveAll(ctx, envVars, nil)
	if err != nil {
		return nil, usererr.New(fmt.Sprintf("resolve secrets: %s", err), err)
	}
	return resolved, nil
}

// stdinIsTerminal returns true when stdin is an interactive terminal (not a pipe).
func stdinIsTerminal(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return false // injected reader (e.g. tests)
	}
	fi, err := f.Stat()
	if err != nil {
		return true
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// resolveEnvironment reads the environment spec from stdin (if piped) or
// from .chunk/config.json in the given directory.
func resolveEnvironment(cmd *cobra.Command, dir string) (envbuilder.Environment, error) {
	var env envbuilder.Environment
	if !stdinIsTerminal(cmd) {
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return env, fmt.Errorf("read environment spec: %w", err)
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return env, fmt.Errorf("parse environment spec: %w", err)
		}
		return env, nil
	}

	detectDir := dir
	if detectDir == "" {
		detectDir = "."
	}
	projCfg, err := config.LoadProjectConfig(detectDir)
	if err != nil {
		return env, fmt.Errorf("no config found — run 'chunk sandbox env' first: %w", err)
	}
	if projCfg.Environment == nil {
		return env, fmt.Errorf("no environment in .chunk/config.json — run 'chunk sandbox env' first")
	}
	if err := json.Unmarshal(projCfg.Environment, &env); err != nil {
		return env, fmt.Errorf("parse environment from config: %w", err)
	}
	return env, nil
}

func newSandboxEnvSetupCmd() *cobra.Command {
	var orgID, name, image, dir, identityFile, envFile string
	var envVarsFlag []string

	cmd := &cobra.Command{
		Use:   "setup [flags] [-- command...]",
		Short: "Create a sandbox, run setup steps, then execute a command or open a shell",
		Long: `Create a sandbox from an image or template, run the environment setup
steps from .chunk/config.json (written by 'chunk sandbox env'), then either
run the given command or drop into an interactive SSH session.

The environment spec is read from .chunk/config.json by default. You can
override it by piping a JSON spec from stdin.

Example:
  chunk sandbox env --dir .                                # detect and save
  chunk sandbox env setup --name my-sandbox -- make test   # read from config
  chunk sandbox env | chunk sandbox env setup --name my-sandbox`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			streams := iostream.FromCmd(cmd)
			resolvedOrgID, err := resolveOrgID(orgID)
			if err != nil {
				return err
			}

			env, err := resolveEnvironment(cmd, dir)
			if err != nil {
				return err
			}

			// Resolve image from flags or env spec.
			// For e2b the image is a template ID — the env spec image is a Docker
			// image name and is not valid here. Only use the env spec image for
			// non-e2b providers, or when --image is explicitly set.
			resolvedProvider := resolveProvider()
			if image == "" && resolvedProvider != "e2b" {
				image = env.ResolvedImage()
				if image == "" {
					return fmt.Errorf("--image is required when environment has no image")
				}
			}
			envVars, err := resolveEnvVars(cmd.Context(), envVarsFlag, envFile)
			if err != nil {
				return err
			}

			// Create sandbox.
			client, err := circleci.NewClient()
			if err != nil {
				return err
			}
			streams.ErrPrintf("Creating sandbox...\n")
			sb, err := sandbox.Create(cmd.Context(), client, resolvedOrgID, name, resolvedProvider, image)
			if err != nil {
				return usererr.New(fmt.Sprintf("Failed to create sandbox. Check your network connection and that --org-id is correct.\n  %s", err), err)
			}
			streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Created sandbox %s (%s)", sb.Name, sb.ID)))

			// Run setup steps via exec API.
			if len(env.Steps) > 0 {
				streams.ErrPrintf("Running %d setup steps...\n", len(env.Steps))
				for i, s := range env.Steps {
					streams.ErrPrintf("[%d/%d] %s\n", i+1, len(env.Steps), s.Name)
					result, err := sandbox.RunStep(cmd.Context(), client, sb.ID, s)
					if err != nil {
						return usererr.New(fmt.Sprintf("Setup failed (sandbox %s still running).\n  %s", sb.ID, err), err)
					}
					if result.Stdout != "" {
						_, _ = fmt.Fprint(streams.Out, result.Stdout)
					}
					if result.Stderr != "" {
						_, _ = fmt.Fprint(streams.Err, result.Stderr)
					}
					if result.ExitCode != 0 {
						streams.ErrPrintf("Sandbox %s is still running. Clean up with: chunk sandbox delete --sandbox-id %s\n", sb.ID, sb.ID)
						return &sandbox.ExitError{Code: result.ExitCode}
					}
				}
				streams.ErrPrintf("%s\n", ui.Success("Setup steps completed"))
			}

			// Run command via exec API, or drop into interactive SSH shell.
			if len(args) > 0 {
				command := sandbox.ShellJoin(args)
				resp, err := sandbox.Exec(cmd.Context(), client, sb.ID, "bash", []string{"-l", "-c", command})
				if err != nil {
					return usererr.New(fmt.Sprintf("Failed to execute command in sandbox.\n  %s", err), err)
				}
				if resp.Stdout != "" {
					_, _ = fmt.Fprint(streams.Out, resp.Stdout)
				}
				if resp.Stderr != "" {
					_, _ = fmt.Fprint(streams.Err, resp.Stderr)
				}
				if resp.ExitCode != 0 {
					return &sandbox.ExitError{Code: resp.ExitCode}
				}
				return nil
			}
			authSock := os.Getenv("SSH_AUTH_SOCK")
			if err := sandbox.SSH(cmd.Context(), client, sb.ID, identityFile, authSock, nil, envVars, streams); err != nil {
				return usererr.New(fmt.Sprintf("Failed to open SSH session to sandbox.\n  %s", err), err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&name, "name", "", "Sandbox name")
	cmd.Flags().StringVar(&image, "image", "", `Container image (unikraft) or E2B template ID (e2b)`)
	cmd.Flags().StringVar(&dir, "dir", "", "Directory to detect environment in (default: current directory)")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file")
	cmd.Flags().StringArrayVarP(&envVarsFlag, "env", "e", nil, "KEY=VALUE pairs to set in the remote session (repeatable)")
	cmd.Flags().StringVar(&envFile, "env-file", "", "Env file to load (default .env.local when flag is present)")
	cmd.Flags().Lookup("env-file").NoOptDefVal = ".env.local"
	_ = cmd.MarkFlagRequired("name")

	return cmd
}
