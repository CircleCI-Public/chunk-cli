package buildprompt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// maxCommentsPerReviewer returns the maximum comment count across all groups.
func maxCommentsPerReviewer(groups []ReviewerGroup) int {
	max := 0
	for _, g := range groups {
		if g.TotalComments > max {
			max = g.TotalComments
		}
	}
	return max
}

// analyzeWithRetry attempts analysis, binary-searching for a viable comment
// limit when the prompt exceeds the model's context window.
func analyzeWithRetry(ctx context.Context, client *anthropic.Client, groups []ReviewerGroup, opts Options, streams iostream.Streams) (string, error) {
	minComments := 1
	currentMax := opts.MaxComments
	if currentMax <= 0 {
		currentMax = maxCommentsPerReviewer(groups)
	}
	currentLimit := currentMax

	for {
		groupsToAnalyze := groups
		if currentLimit < maxCommentsPerReviewer(groups) {
			groupsToAnalyze = LimitCommentsPerReviewer(groups, currentLimit)
		}

		prompt := BuildAnalysisPrompt(groupsToAnalyze)
		estimatedTokens := EstimateTokenCount(prompt)
		totalComments := 0
		for _, g := range groupsToAnalyze {
			totalComments += g.TotalComments
		}

		streams.ErrPrintf("  %s\n", ui.Dim(fmt.Sprintf("Sending %d comments (~%d tokens)", totalComments, estimatedTokens)))

		analysis, err := client.AnalyzeReviews(ctx, prompt, opts.AnalyzeModel)
		if err == nil {
			return analysis, nil
		}

		if !anthropic.IsTokenLimitError(err) {
			return "", err
		}

		// Binary search for a viable limit
		currentMax = currentLimit
		currentLimit = (minComments + currentMax) / 2
		if currentLimit < minComments || currentLimit == currentMax {
			return "", err
		}

		streams.ErrPrintf("  %s\n", ui.Warning(fmt.Sprintf("Token limit exceeded, reducing to %d comments per reviewer...", currentLimit)))
	}
}

// Run executes the full build-prompt pipeline: discover, analyze, generate.
func Run(ctx context.Context, opts Options, streams iostream.Streams) error {
	paths := DeriveOutputPaths(opts.OutputPath)

	// --- Step 1: Discover top reviewers ---
	streams.ErrPrintln(ui.Step(1, 3, "Discovering Top Reviewers"))

	ghClient, err := github.New()
	if err != nil {
		return err
	}

	if err := ghClient.ValidateOrg(ctx, opts.Org); err != nil {
		return err
	}

	if err := ghClient.CheckRateLimit(ctx); err != nil {
		return err
	}

	repos, err := ghClient.FetchOrgRepos(ctx, opts.Org, opts.Repos)
	if err != nil {
		return err
	}

	if len(repos) == 0 {
		streams.ErrPrintln(ui.Warning("No repositories found."))
		return nil
	}

	var allActivities []map[string]*github.UserActivity
	var allDetails [][]github.ReviewCommentDetail

	for i, repo := range repos {
		streams.ErrPrintf("  %s %s\n", ui.Dim(fmt.Sprintf("[%d/%d]", i+1, len(repos))), ui.Bold(repo))
		result, err := ghClient.FetchReviewActivity(ctx, opts.Org, repo, opts.Since)
		if err != nil {
			if github.IsResolutionError(err) {
				streams.ErrPrintf("  %s\n", ui.Warning(fmt.Sprintf("Skipping %s: %v", repo, err)))
				continue
			}
			return err
		}
		if len(result.Activity) > 0 {
			allActivities = append(allActivities, result.Activity)
		}
		if len(result.Details) > 0 {
			allDetails = append(allDetails, result.Details)
		}
	}

	aggregated := AggregateActivity(allActivities)
	topReviewers, err := TopN(aggregated, opts.Top)
	if err != nil {
		return err
	}

	aggregatedDetails := AggregateDetails(allDetails)
	filteredDetails := FilterDetailsByReviewers(aggregatedDetails, topReviewers)

	if err := WriteDetailsJSON(filteredDetails, paths.DetailsPath, opts.Org, opts.Since, len(repos)); err != nil {
		return fmt.Errorf("write details JSON: %w", err)
	}

	prRankings := AggregatePRRankings(filteredDetails)
	if err := WritePRRankingsCSV(prRankings, paths.CSVPath); err != nil {
		return fmt.Errorf("write PR rankings CSV: %w", err)
	}

	streams.ErrPrintf("  %s\n", ui.Success(fmt.Sprintf("Details written to %s", paths.DetailsPath)))
	streams.ErrPrintf("  %s\n", ui.Success(fmt.Sprintf("PR rankings written to %s", paths.CSVPath)))

	// --- Step 2: Analyze review patterns ---
	streams.ErrPrintln(ui.Step(2, 3, "Analyzing Review Patterns"))

	anthropicClient, err := anthropic.New()
	if err != nil {
		return err
	}

	reviewerGroups := GroupByReviewer(filteredDetails)
	if opts.MaxComments > 0 {
		reviewerGroups = LimitCommentsPerReviewer(reviewerGroups, opts.MaxComments)
	}

	analysis, err := analyzeWithRetry(ctx, anthropicClient, reviewerGroups, opts, streams)
	if err != nil {
		return fmt.Errorf("analyze reviews: %w", err)
	}

	var reviewerNames []string
	for _, g := range reviewerGroups {
		reviewerNames = append(reviewerNames, g.Reviewer)
	}

	report := FormatMarkdownReport(analysis, paths.DetailsPath, len(filteredDetails), reviewerNames)

	if err := os.MkdirAll(filepath.Dir(paths.AnalysisPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(paths.AnalysisPath, []byte(report), 0o644); err != nil {
		return fmt.Errorf("write analysis: %w", err)
	}
	streams.ErrPrintf("  %s\n", ui.Success(fmt.Sprintf("Analysis written to %s", paths.AnalysisPath)))

	// --- Step 3: Generate review prompt ---
	streams.ErrPrintln(ui.Step(3, 3, "Generating PR Review Prompt"))

	analysisContent, err := os.ReadFile(paths.AnalysisPath)
	if err != nil {
		return fmt.Errorf("read analysis: %w", err)
	}

	generatedPrompt, err := anthropicClient.GenerateReviewPrompt(ctx, string(analysisContent), opts.PromptModel, opts.IncludeAttribution)
	if err != nil {
		return fmt.Errorf("generate prompt: %w", err)
	}

	footer := fmt.Sprintf("\n\n---\n\n*Generated: %s*\n*Source: %s*\n*Model: %s*", time.Now().Format(time.RFC3339), paths.DetailsPath, opts.PromptModel)

	if err := os.MkdirAll(filepath.Dir(paths.PromptPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(paths.PromptPath, []byte(generatedPrompt+footer), 0o644); err != nil {
		return fmt.Errorf("write prompt: %w", err)
	}
	streams.ErrPrintf("  %s\n", ui.Success(fmt.Sprintf("Prompt written to %s", paths.PromptPath)))

	return nil
}
