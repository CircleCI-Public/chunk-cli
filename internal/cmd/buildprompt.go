package cmd

import "github.com/spf13/cobra"

func newBuildPromptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build-prompt",
		Short: "Generate a review prompt from GitHub PR review patterns",
	}
}
