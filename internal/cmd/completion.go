package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/closer"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
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
	shell := os.Getenv(config.EnvShell)
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
		return shellConfig{}, &userError{
			msg:        "Unsupported shell.",
			suggestion: "Set SHELL to bash or zsh.",
			errMsg:     fmt.Sprintf("unsupported shell %q", shell),
		}
	}
}

// completionInstalled reports whether the completion tag is already in the
// user's shell rc file. Returns error if shell is unsupported or HOME unset.
func completionInstalled() (bool, error) {
	home := os.Getenv(config.EnvHome)
	if home == "" {
		return false, &userError{msg: msgHomeNotSet, errMsg: errMsgHomeNotSet}
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
	home := os.Getenv(config.EnvHome)
	if home == "" {
		return &userError{msg: msgHomeNotSet, errMsg: errMsgHomeNotSet}
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
		return &userError{
			msg:        fmt.Sprintf("Could not update %s.", sh.rcFile),
			suggestion: suggestionCheckPerms,
			err:        err,
		}
	}
	defer closer.ErrorHandler(f, &err)

	if _, err := f.WriteString("\n" + line); err != nil {
		return &userError{
			msg:        fmt.Sprintf("Could not update %s.", sh.rcFile),
			suggestion: suggestionCheckPerms,
			err:        err,
		}
	}

	streams.ErrPrintln(ui.Success("Completion installed."))
	return nil
}

func maybeInstallCompletions(streams iostream.Streams) {
	installed, err := completionInstalled()
	if err != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Skipping shell completions: %v", err)))
		return
	}
	if installed {
		return
	}
	yes, confirmErr := tui.Confirm("Install shell completions?", true)
	if confirmErr != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not confirm: %v", confirmErr)))
		return
	}
	if yes {
		if installErr := installCompletion(streams); installErr != nil {
			streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not install completions: %v", installErr)))
		}
	}
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
	var outputPath string
	cmd := &cobra.Command{
		Use:    "zsh",
		Short:  "Generate zsh completion script",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w, closeOut, err := openCompletionOutput(outputPath, iostream.FromCmd(cmd).Out)
			if err != nil {
				return err
			}
			defer func() { _ = closeOut() }()
			return cmd.Root().GenZshCompletion(w)
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Write completion script to this file instead of stdout")
	return cmd
}

func newCompletionBashCmd() *cobra.Command {
	var outputPath string
	cmd := &cobra.Command{
		Use:    "bash",
		Short:  "Generate bash completion script",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w, closeOut, err := openCompletionOutput(outputPath, iostream.FromCmd(cmd).Out)
			if err != nil {
				return err
			}
			defer func() { _ = closeOut() }()
			return cmd.Root().GenBashCompletion(w)
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Write completion script to this file instead of stdout")
	return cmd
}

// openCompletionOutput resolves the output destination for a completion
// command. When path is empty it returns w (the command's stdout) with a
// no-op closer; otherwise it creates the file and any missing parent
// directories and returns it with its Close method.
func openCompletionOutput(path string, w io.Writer) (io.Writer, func() error, error) {
	if path == "" {
		return w, func() error { return nil }, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create output directory: %w", err)
	}
	f, err := os.Create(path) //#nosec:G304 // path is user-supplied
	if err != nil {
		return nil, nil, fmt.Errorf("create output file: %w", err)
	}
	return f, f.Close, nil
}

func newCompletionUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove shell completion",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			home := os.Getenv(config.EnvHome)
			if home == "" {
				return &userError{msg: msgHomeNotSet, errMsg: errMsgHomeNotSet}
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
				return &userError{
					msg:        fmt.Sprintf("Could not update %s.", sh.rcFile),
					suggestion: suggestionCheckPerms,
					err:        err,
				}
			}

			io.ErrPrintln(ui.Success("Completion uninstalled."))
			return nil
		},
	}
}
