package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sandbox"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newSandboxesCmd() *cobra.Command {
	authSock := os.Getenv("SSH_AUTH_SOCK")

	cmd := &cobra.Command{
		Use:   "sandboxes",
		Short: "Manage sandboxes",
	}

	cmd.AddCommand(newSandboxesListCmd())
	cmd.AddCommand(newSandboxesCreateCmd())
	cmd.AddCommand(newSandboxesExecCmd())
	cmd.AddCommand(newSandboxesAddSSHKeyCmd())
	cmd.AddCommand(newSandboxesSSHCmd(authSock))
	cmd.AddCommand(newSandboxesSyncCmd(authSock))
	cmd.AddCommand(newSandboxesPrepareCmd())

	return cmd
}

func newSandboxesListCmd() *cobra.Command {
	var orgID string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sandboxes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			client, err := circleci.NewClient()
			if err != nil {
				return err
			}
			sandboxes, err := sandbox.List(cmd.Context(), client, orgID)
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
	_ = cmd.MarkFlagRequired("org-id")

	return cmd
}

func newSandboxesCreateCmd() *cobra.Command {
	var orgID, name, image string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a sandbox",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			client, err := circleci.NewClient()
			if err != nil {
				return err
			}
			sb, err := sandbox.Create(cmd.Context(), client, orgID, name, image)
			if err != nil {
				return err
			}
			io.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Created sandbox %s (%s)", sb.Name, sb.ID)))
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&name, "name", "", "Sandbox name")
	cmd.Flags().StringVar(&image, "image", "", "Container image")
	_ = cmd.MarkFlagRequired("org-id")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func newSandboxesExecCmd() *cobra.Command {
	var orgID, sandboxID, command string
	var execArgs []string

	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Execute a command in a sandbox",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
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

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID")
	cmd.Flags().StringVar(&command, "command", "", "Command to execute")
	cmd.Flags().StringArrayVar(&execArgs, "args", nil, "Command arguments")
	_ = cmd.MarkFlagRequired("org-id")
	_ = cmd.MarkFlagRequired("sandbox-id")
	_ = cmd.MarkFlagRequired("command")

	return cmd
}

func newSandboxesAddSSHKeyCmd() *cobra.Command {
	var orgID, sandboxID, publicKey, publicKeyFile string

	cmd := &cobra.Command{
		Use:   "add-ssh-key",
		Short: "Add an SSH public key to a sandbox",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
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

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID")
	cmd.Flags().StringVar(&publicKey, "public-key", "", "SSH public key string")
	cmd.Flags().StringVar(&publicKeyFile, "public-key-file", "", "Path to SSH public key file")
	_ = cmd.MarkFlagRequired("org-id")
	_ = cmd.MarkFlagRequired("sandbox-id")

	return cmd
}

func newSandboxesSSHCmd(authSock string) *cobra.Command {
	var orgID, sandboxID, identityFile string

	cmd := &cobra.Command{
		Use:   "ssh [flags] [-- command...]",
		Short: "SSH into a sandbox",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			client, err := circleci.NewClient()
			if err != nil {
				return err
			}
			return sandbox.SSH(cmd.Context(), client, sandboxID, identityFile, authSock, args, io)
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file")
	_ = cmd.MarkFlagRequired("org-id")
	_ = cmd.MarkFlagRequired("sandbox-id")

	return cmd
}

func newSandboxesSyncCmd(authSock string) *cobra.Command {
	var orgID, sandboxID, dest, identityFile string
	var bootstrap bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync files to a sandbox",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			client, err := circleci.NewClient()
			if err != nil {
				return err
			}
			return sandbox.Sync(cmd.Context(), client, sandboxID, identityFile, authSock, dest, bootstrap, io)
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID")
	cmd.Flags().StringVar(&dest, "dest", "/workspace", "Destination path")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file")
	cmd.Flags().BoolVar(&bootstrap, "bootstrap", false, "Bootstrap the sandbox")
	_ = cmd.MarkFlagRequired("org-id")
	_ = cmd.MarkFlagRequired("sandbox-id")

	return cmd
}

func newSandboxesPrepareCmd() *cobra.Command {
	var dockerSudo bool

	cmd := &cobra.Command{
		Use:   "prepare",
		Short: "Prepare sandbox environment",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Check we're in a git repo
			gitCmd := exec.Command("git", "rev-parse", "--git-dir")
			gitCmd.Dir = "."
			if err := gitCmd.Run(); err != nil {
				return fmt.Errorf("not a git repository")
			}

			claude, err := anthropic.New()
			if err != nil {
				return err
			}

			io := iostream.FromCmd(cmd)
			return sandbox.Prepare(cmd.Context(), claude, dockerSudo, io, os.Stdin)
		},
	}

	cmd.Flags().BoolVar(&dockerSudo, "docker-sudo", false, "Use sudo for docker commands")

	return cmd
}
