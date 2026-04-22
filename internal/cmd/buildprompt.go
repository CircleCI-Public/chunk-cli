package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/buildprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newBuildPromptCmd() *cobra.Command {
	var (
		org                string
		repos              string
		top                int
		since              string
		output             string
		maxComments        int
		analyzeModel       string
		promptModel        string
		includeAttribution bool
	)

	cmd := &cobra.Command{
		Use:   "build-prompt",
		Short: "Analyze GitHub PR comments and generate a review prompt for AI coding agents",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if top <= 0 {
				return &userError{msg: "--top must be a positive integer.", errMsg: fmt.Sprintf("invalid --top value: %d", top)}
			}

			cwd, err := os.Getwd()
			if err != nil {
				return &userError{msg: "Could not determine working directory.", err: err}
			}
			resolvedOrg, resolvedRepos, err := buildprompt.ResolveOrgAndRepos(org, repos, cwd)
			if err != nil {
				return &userError{msg: "Could not determine org and repos.", suggestion: "Use --org and --repos flags.", err: err}
			}

			sinceTime, err := parseSince(since)
			if err != nil {
				return err
			}

			if analyzeModel == "" {
				analyzeModel = config.AnalyzeModel
			}
			if promptModel == "" {
				promptModel = config.PromptModel
			}

			// Warn about legacy output paths
			streams := iostream.FromCmd(cmd)
			for _, legacy := range []string{"./review-prompt.md", ".chunk/review-prompt.md"} {
				if _, err := os.Stat(legacy); err == nil {
					streams.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Found legacy output at %s — default output is now %s", legacy, output)))
				}
			}

			ghClient, err := ensureGitHubClient(cmd.Context(), streams, tui.PromptHidden)
			if err != nil {
				return err
			}

			anthropicClient, err := ensureAnthropicClient(cmd.Context(), streams, tui.PromptHidden)
			if err != nil {
				return err
			}

			opts := buildprompt.Options{
				Org:                resolvedOrg,
				Repos:              resolvedRepos,
				Top:                top,
				Since:              sinceTime,
				OutputPath:         output,
				MaxComments:        maxComments,
				AnalyzeModel:       analyzeModel,
				PromptModel:        promptModel,
				IncludeAttribution: includeAttribution,
				Status:             newStatusFunc(streams),
			}

			return buildprompt.Run(cmd.Context(), opts, ghClient, anthropicClient)
		},
	}

	cmd.Flags().StringVar(&org, "org", "", "GitHub organization")
	cmd.Flags().StringVar(&repos, "repos", "", "Comma-separated repository names")
	cmd.Flags().IntVar(&top, "top", 5, "Number of top reviewers to include")
	cmd.Flags().StringVar(&since, "since", defaultSince(), "Start date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&output, "output", ".chunk/context/review-prompt.md", "Output file path")
	cmd.Flags().IntVar(&maxComments, "max-comments", 0, "Max comments per reviewer (0 = no limit)")
	cmd.Flags().StringVar(&analyzeModel, "analyze-model", "", "Model for analysis step")
	cmd.Flags().StringVar(&promptModel, "prompt-model", "", "Model for prompt generation step")
	cmd.Flags().BoolVar(&includeAttribution, "include-attribution", false, "Include reviewer attribution in output")

	return cmd
}

func parseSince(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}

func defaultSince() string {
	return time.Now().AddDate(0, -3, 0).Format("2006-01-02")
}
