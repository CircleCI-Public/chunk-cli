package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/cmd/usererr"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/closer"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

const completionTag = "# chunk shell completion"

func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion",
		Short: "Manage shell completions",
	}

	cmd.AddCommand(newCompletionInstallCmd())
	cmd.AddCommand(newCompletionUninstallCmd())
	cmd.AddCommand(newCompletionZshCmd())
	cmd.AddCommand(newCompletionBashCmd())
	return cmd
}

type shellConfig struct {
	name   string
	rcFile string
	source string
}

func detectShell(home string) (shellConfig, error) {
	shell := os.Getenv("SHELL")
	switch {
	case strings.HasSuffix(shell, "zsh"):
		return shellConfig{
			name:   "zsh",
			rcFile: filepath.Join(home, ".zshrc"),
			source: "source <(chunk completion zsh)",
		}, nil
	case strings.HasSuffix(shell, "bash"):
		rcFile := filepath.Join(home, ".bash_profile")
		if _, err := os.Stat(filepath.Join(home, ".bashrc")); err == nil {
			rcFile = filepath.Join(home, ".bashrc")
		}
		return shellConfig{
			name:   "bash",
			rcFile: rcFile,
			source: "source <(chunk completion bash)",
		}, nil
	default:
		return shellConfig{}, usererr.New("Unsupported shell: set SHELL to bash or zsh.", fmt.Errorf("unsupported shell %q", shell))
	}
}

// completionInstalled reports whether the completion tag is already in the
// user's shell rc file. Returns error if shell is unsupported or HOME unset.
func completionInstalled() (bool, error) {
	home := os.Getenv("HOME")
	if home == "" {
		return false, usererr.Newf("HOME environment variable is not set.", "HOME not set")
	}

	sh, err := detectShell(home)
	if err != nil {
		return false, err
	}

	data, err := os.ReadFile(sh.rcFile)
	if err != nil {
		return false, nil // rc file doesn't exist — not installed
	}
	return strings.Contains(string(data), completionTag), nil
}

// installCompletion appends the completion source line to the user's shell rc file.
func installCompletion(streams iostream.Streams) (err error) {
	home := os.Getenv("HOME")
	if home == "" {
		return usererr.Newf("HOME environment variable is not set.", "HOME not set")
	}

	sh, err := detectShell(home)
	if err != nil {
		return err
	}

	line := completionTag + "\n" + sh.source + "\n"

	// Check if already installed.
	data, readErr := os.ReadFile(sh.rcFile)
	if readErr == nil && strings.Contains(string(data), completionTag) {
		streams.ErrPrintln(ui.Warning("Completion already installed."))
		return nil
	}

	f, err := os.OpenFile(sh.rcFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return usererr.New(fmt.Sprintf("Could not update %s. Check file permissions.", sh.rcFile), err)
	}
	defer closer.ErrorHandler(f, &err)

	if _, err := f.WriteString("\n" + line); err != nil {
		return usererr.New(fmt.Sprintf("Could not update %s. Check file permissions.", sh.rcFile), err)
	}

	streams.ErrPrintln(ui.Success("Completion installed."))
	return nil
}

func newCompletionInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install shell completion",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return installCompletion(iostream.FromCmd(cmd))
		},
	}
}

func newCompletionZshCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "zsh",
		Short:  "Generate zsh completion script",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Root().GenZshCompletion(iostream.FromCmd(cmd).Out)
		},
	}
}

func newCompletionBashCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "bash",
		Short:  "Generate bash completion script",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Root().GenBashCompletion(iostream.FromCmd(cmd).Out)
		},
	}
}

func newCompletionUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove shell completion",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			home := os.Getenv("HOME")
			if home == "" {
				return usererr.Newf("HOME environment variable is not set.", "HOME not set")
			}

			sh, err := detectShell(home)
			if err != nil {
				return err
			}

			data, err := os.ReadFile(sh.rcFile)
			if err != nil {
				// Nothing to uninstall
				io.ErrPrintln(ui.Success("Completion uninstalled."))
				return nil
			}

			var lines []string
			scanner := bufio.NewScanner(strings.NewReader(string(data)))
			skip := false
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, completionTag) {
					skip = true
					continue
				}
				if skip && strings.Contains(line, "source <(chunk completion") {
					skip = false
					continue
				}
				skip = false
				lines = append(lines, line)
			}

			if err := os.WriteFile(sh.rcFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				return usererr.New(fmt.Sprintf("Could not update %s. Check file permissions.", sh.rcFile), err)
			}

			io.ErrPrintln(ui.Success("Completion uninstalled."))
			return nil
		},
	}
}
