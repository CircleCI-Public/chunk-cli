package cmd

import (
	"github.com/spf13/cobra"
)

func NewRootCmd(version string) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "chunk",
		Short:         "Generate AI review context and trigger AI coding tasks",
		Version:       version,
		SilenceErrors: true,
	}

	rootCmd.SetHelpTemplate(rootCmd.HelpTemplate() + `
Getting started:
  chunk init                    Initialize project configuration
  chunk auth login              Save your Anthropic API key
  chunk build-prompt            Generate a review prompt from GitHub PR comments
  chunk task config             Set up CircleCI task configuration
  chunk task run --definition <name> --prompt "<task>"
                                Trigger an AI coding task
`)

	rootCmd.AddCommand(newInitCmd())
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
	rootCmd.AddCommand(newCommandsCmd())

	return rootCmd
}
