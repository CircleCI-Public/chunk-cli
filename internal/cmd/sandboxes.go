package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sandbox"
)

func newSandboxesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandboxes",
		Short: "Manage sandboxes",
	}

	cmd.AddCommand(newSandboxesListCmd())
	cmd.AddCommand(newSandboxesCreateCmd())
	cmd.AddCommand(newSandboxesExecCmd())
	cmd.AddCommand(newSandboxesAddSshKeyCmd())
	cmd.AddCommand(newSandboxesSshCmd())
	cmd.AddCommand(newSandboxesSyncCmd())
	cmd.AddCommand(newSandboxesPrepareCmd())

	return cmd
}

func newSandboxesListCmd() *cobra.Command {
	var orgID string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sandboxes",
		RunE: func(cmd *cobra.Command, args []string) error {
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
				io.ErrPrintln("No sandboxes found")
				return nil
			}
			for _, s := range sandboxes {
				io.Printf("%s  %s\n", s.ID, s.Name)
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
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			client, err := circleci.NewClient()
			if err != nil {
				return err
			}
			sb, err := sandbox.Create(cmd.Context(), client, orgID, name, image)
			if err != nil {
				return err
			}
			io.ErrPrintf("Created sandbox %s (%s)\n", sb.Name, sb.ID)
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
			allArgs := append(execArgs, args...)
			resp, err := sandbox.Exec(cmd.Context(), client, orgID, sandboxID, command, allArgs)
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

func newSandboxesAddSshKeyCmd() *cobra.Command {
	var orgID, sandboxID, publicKey, publicKeyFile string

	cmd := &cobra.Command{
		Use:   "add-ssh-key",
		Short: "Add an SSH public key to a sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			client, err := circleci.NewClient()
			if err != nil {
				return err
			}
			resp, err := sandbox.AddSshKey(cmd.Context(), client, sandboxID, publicKey, publicKeyFile)
			if err != nil {
				return err
			}
			io.ErrPrintf("SSH key added. Sandbox URL: %s\n", resp.URL)
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

func newSandboxesSshCmd() *cobra.Command {
	var orgID, sandboxID, identityFile string

	cmd := &cobra.Command{
		Use:   "ssh",
		Short: "SSH into a sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := circleci.NewClient()
			if err != nil {
				return err
			}
			token, err := client.CreateAccessToken(cmd.Context(), sandboxID)
			if err != nil {
				return err
			}
			// SSH connection would go here; for now fail indicating not fully implemented
			_ = token
			_ = identityFile
			return fmt.Errorf("ssh connection not yet implemented")
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file")
	_ = cmd.MarkFlagRequired("org-id")
	_ = cmd.MarkFlagRequired("sandbox-id")

	return cmd
}

func newSandboxesSyncCmd() *cobra.Command {
	var orgID, sandboxID, dest, identityFile string
	var bootstrap bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync files to a sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := circleci.NewClient()
			if err != nil {
				return err
			}
			token, err := client.CreateAccessToken(cmd.Context(), sandboxID)
			if err != nil {
				return err
			}
			_ = token
			_ = dest
			_ = identityFile
			_ = bootstrap
			return fmt.Errorf("sync not yet implemented")
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID")
	cmd.Flags().StringVar(&dest, "dest", "", "Destination path")
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
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check we're in a git repo
			gitCmd := exec.Command("git", "rev-parse", "--git-dir")
			gitCmd.Dir = "."
			if err := gitCmd.Run(); err != nil {
				return fmt.Errorf("not a git repository")
			}

			apiKey := os.Getenv("ANTHROPIC_API_KEY")
			if apiKey == "" {
				return fmt.Errorf("ANTHROPIC_API_KEY environment variable is required")
			}

			_ = dockerSudo
			return fmt.Errorf("prepare not yet implemented")
		},
	}

	cmd.Flags().BoolVar(&dockerSudo, "docker-sudo", false, "Use sudo for docker commands")

	return cmd
}

