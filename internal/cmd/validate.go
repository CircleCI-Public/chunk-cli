package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sandbox"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/usererr"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

func newValidateCmd() *cobra.Command {
	var sandboxID, identityFile, workdir string
	var dryRun, list, save, forceRun, status bool
	var inlineCmd, projectDir string

	cmd := &cobra.Command{
		Use:          "validate [name]",
		Short:        "Run validation commands",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			streams := iostream.FromCmd(cmd)

			workDir := projectDir
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return err
				}
			}

			var name string
			if len(args) == 1 {
				name = args[0]
			}

			// --list: show configured commands
			if list {
				cfg, err := config.LoadProjectConfig(workDir)
				if err != nil {
					cfg = &config.ProjectConfig{}
				}
				return validate.List(cfg, streams)
			}

			// --status: check cache only
			if status {
				cfg, err := config.LoadProjectConfig(workDir)
				if err != nil {
					cfg = &config.ProjectConfig{}
				}
				return validate.Status(workDir, name, cfg, streams)
			}

			// --cmd: run inline command
			if inlineCmd != "" {
				cmdName := name
				if cmdName == "" {
					cmdName = "custom"
				}
				if dryRun {
					streams.Printf("%s: %s\n", ui.Bold(cmdName), ui.Gray(inlineCmd))
					return nil
				}
				if save {
					if err := config.SaveCommand(workDir, cmdName, inlineCmd); err != nil {
						return usererr.New("Could not save command to .chunk/config.json.", err)
					}
					streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", cmdName)))
				}
				return validate.RunInline(cmd.Context(), workDir, cmdName, inlineCmd, forceRun, streams)
			}

			cfg, err := config.LoadProjectConfig(workDir)
			if err != nil || !cfg.HasCommands() {
				return usererr.New(
					"No validate commands configured. Run 'chunk init' first.",
					fmt.Errorf("no validate commands configured"),
				)
			}

			if dryRun {
				return validate.RunDryRun(cfg, name, streams)
			}

			if sandboxID != "" {
				client, err := ensureCircleCIClient(cmd.Context(), streams, tui.PromptHidden)
				if err != nil {
					return err
				}
				authSock := os.Getenv("SSH_AUTH_SOCK")
				session, err := sandbox.OpenSession(cmd.Context(), client, sandboxID, identityFile, authSock)
				if err != nil {
					return usererr.New("Could not open SSH session to sandbox.", err)
				}
				dest := workdir
				if dest == "" {
					dest = "/workspace"
				}
				return validate.RunRemote(cmd.Context(), func(ctx context.Context, script string) (string, string, int, error) {
					result, err := sandbox.ExecOverSSH(ctx, session, "sh -c "+sandbox.ShellEscape(script), nil, nil)
					if err != nil {
						return "", "", 0, err
					}
					return result.Stdout, result.Stderr, result.ExitCode, nil
				}, cfg, dest, streams)
			}

			// Named command
			if name != "" {
				if cfg.FindCommand(name) == nil {
					isTTY := term.IsTerminal(int(os.Stdin.Fd()))
					if !isTTY {
						return usererr.New(
							fmt.Sprintf("Command %q is not configured. Add it to .chunk/config.json.", name),
							fmt.Errorf("command %q is not configured", name),
						)
					}
					// Interactive setup: prompt for command
					streams.ErrPrintf("Command %s is not configured yet.\n\n", ui.Bold(name))
					streams.ErrPrintf("What command should %s run? ", ui.Bold(name))
					scanner := bufio.NewScanner(os.Stdin)
					if !scanner.Scan() {
						return usererr.New("No command entered.", fmt.Errorf("no input received"))
					}
					input := strings.TrimSpace(scanner.Text())
					if input == "" {
						streams.ErrPrintln(ui.Dim("No command entered, aborting."))
						return usererr.New("No command entered.", fmt.Errorf("no command entered"))
					}
					if err := config.SaveCommand(workDir, name, input); err != nil {
						return usererr.New("Could not save command to .chunk/config.json.", err)
					}
					streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", name)))
					// Reload config after save
					cfg, err = config.LoadProjectConfig(workDir)
					if err != nil {
						return err
					}
				}
				return validate.RunNamed(cmd.Context(), workDir, name, forceRun, cfg, streams)
			}

			// Run all
			return validate.RunAll(cmd.Context(), workDir, forceRun, cfg, streams)
		},
	}

	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID for remote execution")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file (uses ssh-agent or ~/.ssh/chunk_ai when omitted)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory on sandbox (auto-detected as /workspace/<repo> when omitted)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show commands without executing")
	cmd.Flags().BoolVar(&list, "list", false, "List all configured commands")
	cmd.Flags().StringVar(&inlineCmd, "cmd", "", "Run an inline command instead of config")
	cmd.Flags().BoolVar(&save, "save", false, "Save --cmd to .chunk/config.json")
	cmd.Flags().BoolVar(&forceRun, "force-run", false, "Ignore cache, always run")
	cmd.Flags().BoolVar(&status, "status", false, "Check cache only, don't execute")
	cmd.Flags().StringVar(&projectDir, "project", "", "Override project directory")

	return cmd
}
