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

// shellConfig holds shell-specific paths resolved at detection time.
//
// For bash, rcFile is empty: bash-completion v2 auto-discovers scripts placed
// in $XDG_DATA_HOME/bash-completion/completions/, so no rc file modification
// is needed. completionInstalled / installCompletion / uninstall use file
// presence instead of the rc tag when rcFile == "".
type shellConfig struct {
	name       string
	rcFile     string // empty for shells that use XDG auto-discovery (bash)
	scriptPath string // absolute path to write the completion script
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
		configDir, err := config.Dir()
		if err != nil {
			return shellConfig{}, fmt.Errorf("resolve config dir: %w", err)
		}
		return shellConfig{
			name:       shellZsh,
			rcFile:     filepath.Join(home, ".zshrc"),
			scriptPath: filepath.Join(configDir, "completion.zsh"),
		}, nil
	case strings.HasSuffix(shell, shellBash):
		// bash-completion v2 auto-discovers completions from
		// $XDG_DATA_HOME/bash-completion/completions/ — no rc file needed.
		dataHome := os.Getenv(config.EnvXDGDataHome)
		if dataHome == "" {
			dataHome = filepath.Join(home, ".local", "share")
		}
		return shellConfig{
			name:       shellBash,
			scriptPath: filepath.Join(dataHome, "bash-completion", "completions", "chunk"),
		}, nil
	default:
		return shellConfig{}, &userError{
			msg:        "Unsupported shell.",
			suggestion: "Set SHELL to bash or zsh.",
			errMsg:     fmt.Sprintf("unsupported shell %q", shell),
		}
	}
}

// completionInstalled reports whether completions are installed.
// For zsh, checks for the tag in the rc file.
// For bash, checks whether the script file exists.
func completionInstalled() (bool, error) {
	home := os.Getenv(config.EnvHome)
	if home == "" {
		return false, &userError{msg: msgHomeNotSet, errMsg: errMsgHomeNotSet}
	}

	sh, err := detectShell(home)
	if err != nil {
		return false, err
	}

	if sh.rcFile == "" {
		_, err := os.Stat(sh.scriptPath)
		if os.IsNotExist(err) {
			return false, nil
		}
		return err == nil, err
	}

	data, err := os.ReadFile(sh.rcFile)
	if err != nil {
		return false, nil // rc file doesn't exist — not installed
	}
	return strings.Contains(string(data), completionTag), nil
}

// writeCompletionFile generates and writes the completion script to sh.scriptPath.
func writeCompletionFile(cmd *cobra.Command, sh shellConfig) error {
	if err := os.MkdirAll(filepath.Dir(sh.scriptPath), 0o755); err != nil {
		return fmt.Errorf("create completion dir: %w", err)
	}

	var buf bytes.Buffer
	if err := sh.generate(cmd.Root(), &buf); err != nil {
		return fmt.Errorf("generate completion script: %w", err)
	}

	if err := os.WriteFile(sh.scriptPath, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write completion file: %w", err)
	}

	return nil
}

// installCompletion writes the completion script and, for zsh, appends a
// source line to the rc file. For bash, only the script file is written.
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
	installed, err := completionInstalled()
	if err != nil {
		return err
	}
	if installed {
		streams.ErrPrintln(ui.Warning("Completion already installed."))
		return nil
	}

	if err := writeCompletionFile(cmd, sh); err != nil {
		return err
	}

	// Bash: auto-discovered — no rc modification needed.
	if sh.rcFile == "" {
		streams.ErrPrintln(ui.Success("Completion installed."))
		return nil
	}

	// Zsh: append source line to rc file.
	line := completionTag + "\n" + "source " + sh.scriptPath + "\n"

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

	if err := writeCompletionFile(cmd, sh); err != nil {
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

			// Remove the script file (best-effort).
			_ = os.Remove(sh.scriptPath)

			// Bash: no rc file to clean up.
			if sh.rcFile == "" {
				io.ErrPrintln(ui.Success("Completion uninstalled."))
				return nil
			}

			// Zsh: strip the tag and source line from the rc file.
			data, err := os.ReadFile(sh.rcFile)
			if err != nil {
				// Nothing to clean up in the rc file.
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
					// and the new "source /path/to/completion.zsh" formats.
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

			io.ErrPrintln(ui.Success("Completion uninstalled."))
			return nil
		},
	}
}
