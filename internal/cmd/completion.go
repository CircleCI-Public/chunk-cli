package cmd

import (
	"bufio"
	"bytes"
	"fmt"
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

const (
	completionTag = "# chunk shell completion"
	shellZsh      = "zsh"
	shellBash     = "bash"
)

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
	name          string
	rcFile        string
	completionExt string
}

// generate writes a shell completion script for sh to w.
func (sh shellConfig) generate(rootCmd *cobra.Command, w *bytes.Buffer) error {
	switch sh.name {
	case shellZsh:
		return rootCmd.GenZshCompletion(w)
	case shellBash:
		return rootCmd.GenBashCompletion(w)
	default:
		return fmt.Errorf("unsupported shell: %s", sh.name)
	}
}

func detectShell(home string) (shellConfig, error) {
	shell := os.Getenv(config.EnvShell)
	switch {
	case strings.HasSuffix(shell, shellZsh):
		return shellConfig{
			name:          shellZsh,
			rcFile:        filepath.Join(home, ".zshrc"),
			completionExt: ".zsh",
		}, nil
	case strings.HasSuffix(shell, shellBash):
		rcFile := filepath.Join(home, ".bash_profile")
		if _, err := os.Stat(filepath.Join(home, ".bashrc")); err == nil {
			rcFile = filepath.Join(home, ".bashrc")
		}
		return shellConfig{
			name:          shellBash,
			rcFile:        rcFile,
			completionExt: ".bash",
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

// completionFilePath returns the path to the static completion script for sh,
// stored under the chunk config directory.
func completionFilePath(sh shellConfig) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "completion"+sh.completionExt), nil
}

// writeCompletionFile generates the completion script for sh and writes it to
// the chunk config directory. Returns the path to the written file.
func writeCompletionFile(cmd *cobra.Command, sh shellConfig) (string, error) {
	filePath, err := completionFilePath(sh)
	if err != nil {
		return "", fmt.Errorf("resolve completion file path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return "", fmt.Errorf("create completion dir: %w", err)
	}

	var buf bytes.Buffer
	if err := sh.generate(cmd.Root(), &buf); err != nil {
		return "", fmt.Errorf("generate completion script: %w", err)
	}

	if err := os.WriteFile(filePath, buf.Bytes(), 0o644); err != nil {
		return "", fmt.Errorf("write completion file: %w", err)
	}

	return filePath, nil
}

// installCompletion writes the static completion script and appends a source
// line for it to the user's shell rc file.
func installCompletion(cmd *cobra.Command, streams iostream.Streams) (err error) {
	home := os.Getenv(config.EnvHome)
	if home == "" {
		return &userError{msg: msgHomeNotSet, errMsg: errMsgHomeNotSet}
	}

	sh, err := detectShell(home)
	if err != nil {
		return err
	}

	// Check if already installed.
	data, readErr := os.ReadFile(sh.rcFile)
	if readErr == nil && strings.Contains(string(data), completionTag) {
		streams.ErrPrintln(ui.Warning("Completion already installed."))
		return nil
	}

	filePath, err := writeCompletionFile(cmd, sh)
	if err != nil {
		return err
	}

	line := completionTag + "\n" + "source " + filePath + "\n"

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

func maybeInstallCompletions(cmd *cobra.Command, streams iostream.Streams) {
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
		if installErr := installCompletion(cmd, streams); installErr != nil {
			streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not install completions: %v", installErr)))
		}
	}
}

// RegenerateCompletionIfInstalled rewrites the static completion script after
// an upgrade. It is a no-op if completions are not installed.
func RegenerateCompletionIfInstalled(cmd *cobra.Command, streams iostream.Streams) {
	installed, err := completionInstalled()
	if err != nil || !installed {
		return
	}

	home := os.Getenv(config.EnvHome)
	if home == "" {
		return
	}

	sh, err := detectShell(home)
	if err != nil {
		return
	}

	if _, err := writeCompletionFile(cmd, sh); err != nil {
		streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Could not regenerate completions: %v", err)))
		return
	}

	streams.ErrPrintln(ui.Success("Completions regenerated."))
}

func newCompletionInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install shell completion",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return installCompletion(cmd, iostream.FromCmd(cmd))
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
				if skip {
					// Remove the source line immediately following the tag,
					// handling both the old "source <(chunk completion ...)"
					// and the new "source /path/to/completion.*" formats.
					skip = false
					continue
				}
				lines = append(lines, line)
			}

			if err := os.WriteFile(sh.rcFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				return &userError{
					msg:        fmt.Sprintf("Could not update %s.", sh.rcFile),
					suggestion: suggestionCheckPerms,
					err:        err,
				}
			}

			// Best-effort removal of the static completion file.
			if filePath, err := completionFilePath(sh); err == nil {
				_ = os.Remove(filePath)
			}

			io.ErrPrintln(ui.Success("Completion uninstalled."))
			return nil
		},
	}
}
