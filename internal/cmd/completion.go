package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
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
	return cmd
}

func newCompletionInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install shell completion",
		RunE: func(cmd *cobra.Command, args []string) error {
			home := os.Getenv("HOME")
			if home == "" {
				return fmt.Errorf("HOME not set")
			}

			rcFile := filepath.Join(home, ".zshrc")
			line := completionTag + "\nsource <(chunk completion zsh)\n"

			// Check if already installed
			if data, err := os.ReadFile(rcFile); err == nil {
				if strings.Contains(string(data), completionTag) {
					fmt.Fprintln(cmd.OutOrStdout(), "Completion already installed.")
					return nil
				}
			}

			f, err := os.OpenFile(rcFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return fmt.Errorf("open %s: %w", rcFile, err)
			}
			defer f.Close()

			if _, err := f.WriteString("\n" + line); err != nil {
				return fmt.Errorf("write %s: %w", rcFile, err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "Completion installed.")
			return nil
		},
	}
}

func newCompletionZshCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "zsh",
		Short:  "Generate zsh completion script",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Root().GenZshCompletion(cmd.OutOrStdout())
		},
	}
}

func newCompletionUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove shell completion",
		RunE: func(cmd *cobra.Command, args []string) error {
			home := os.Getenv("HOME")
			if home == "" {
				return fmt.Errorf("HOME not set")
			}

			rcFile := filepath.Join(home, ".zshrc")
			data, err := os.ReadFile(rcFile)
			if err != nil {
				// Nothing to uninstall
				fmt.Fprintln(cmd.OutOrStdout(), "Completion uninstalled.")
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

			if err := os.WriteFile(rcFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", rcFile, err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "Completion uninstalled.")
			return nil
		},
	}
}
