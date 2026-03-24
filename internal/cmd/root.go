package cmd

import (
	"github.com/spf13/cobra"
)

func NewRootCmd(version string) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "chunk",
		Short:   "CLI for generating AI agent context from real code review patterns",
		Version: version,
	}

	rootCmd.AddCommand(newAuthCmd())
	rootCmd.AddCommand(newConfigCmd())
	rootCmd.AddCommand(newBuildPromptCmd())
	rootCmd.AddCommand(newSkillsCmd())
	rootCmd.AddCommand(newCompletionCmd())
	rootCmd.AddCommand(newSandboxesCmd())
	rootCmd.AddCommand(newTaskCmd())
	rootCmd.AddCommand(newValidateCmd())
	rootCmd.AddCommand(newUpgradeCmd())
	rootCmd.AddCommand(newHookCmd())

	return rootCmd
}
