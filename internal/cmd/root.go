package cmd

import (
	"github.com/spf13/cobra"
)

func NewRootCmd(version string) *cobra.Command {
	cobra.EnableTraverseRunHooks = true

	rootCmd := &cobra.Command{
		Use:           "chunk",
		Short:         "Generate AI review context and trigger AI coding tasks",
		Version:       version,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return nil
		},
	}

	rootCmd.SetHelpTemplate(rootCmd.HelpTemplate() + `
Getting started:
  chunk init                    Initialize project configuration
  chunk auth set <provider>     Store credentials (CircleCI token, Anthropic API key)
  chunk build-prompt            Generate a review prompt from GitHub PR comments
  chunk task config             Set up CircleCI task configuration
  chunk task run --definition <name> --prompt "<task>"
                                Trigger an AI coding task

Environment Variables:
  CIRCLECI_TOKEN                  CircleCI API token (also: CIRCLE_TOKEN)
  ANTHROPIC_API_KEY               Anthropic API key
  GITHUB_TOKEN                    GitHub personal access token
  CIRCLECI_ORG_ID                 CircleCI organization ID
  CODE_REVIEW_CLI_MODEL           Claude model override
  CIRCLECI_BASE_URL               CircleCI API URL [default: https://circleci.com]
  ANTHROPIC_BASE_URL              Anthropic API URL [default: https://api.anthropic.com]
  GITHUB_API_URL                  GitHub API URL [default: https://api.github.com]
  SSH_AUTH_SOCK                   SSH agent socket for sidecar key auth
  NO_COLOR                        Disable colored output
  CI                              Disable interactive prompts (set by most CI systems)

Configuration:
  ~/.config/chunk/config.json     User credentials and settings ($XDG_CONFIG_HOME/chunk/config.json)
  .chunk/config.json              Project settings (per repository)
  .chunk/run.json                 Task run configuration (chunk task config)
`)

	rootCmd.AddCommand(newInitCmd())
	rootCmd.AddCommand(newAuthCmd())
	rootCmd.AddCommand(newConfigCmd())
	rootCmd.AddCommand(newBuildPromptCmd())
	rootCmd.AddCommand(newSkillCmd())
	rootCmd.AddCommand(newCompletionCmd())
	rootCmd.AddCommand(newSidecarCmd())
	rootCmd.AddCommand(newTaskCmd())
	rootCmd.AddCommand(newValidateCmd())
	rootCmd.AddCommand(newHookCmd())
	rootCmd.AddCommand(newUpgradeCmd())

	rootCmd.AddCommand(newCommandsCmd())

	rootCmd.PersistentFlags().Bool("insecure-storage", false, "do not use the system's secure storage for storing tokens")
	_ = rootCmd.PersistentFlags().MarkHidden("insecure-storage")

	return rootCmd
}
