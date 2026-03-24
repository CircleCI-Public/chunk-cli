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
)

// Run executes the full build-prompt pipeline: discover, analyze, generate.
func Run(ctx context.Context, opts Options, streams iostream.Streams) error {
	paths := DeriveOutputPaths(opts.OutputPath)

	// --- Step 1: Discover top reviewers ---
	streams.ErrPrintln("Step 1/3: Discovering Top Reviewers")

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
		streams.ErrPrintln("No repositories found.")
		return nil
	}

	var allActivities []map[string]*github.UserActivity
	var allDetails [][]github.ReviewCommentDetail

	for i, repo := range repos {
		streams.ErrPrintf("  [%d/%d] %s\n", i+1, len(repos), repo)
		result, err := ghClient.FetchReviewActivity(ctx, opts.Org, repo, opts.Since)
		if err != nil {
			if github.IsResolutionError(err) {
				streams.ErrPrintf("  Skipping %s: %v\n", repo, err)
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

	streams.ErrPrintf("  Details written to %s\n", paths.DetailsPath)
	streams.ErrPrintf("  PR rankings written to %s\n", paths.CSVPath)

	// --- Step 2: Analyze review patterns ---
	streams.ErrPrintln("Step 2/3: Analyzing Review Patterns")

	anthropicClient, err := anthropic.New()
	if err != nil {
		return err
	}

	reviewerGroups := GroupByReviewer(filteredDetails)
	if opts.MaxComments > 0 {
		reviewerGroups = LimitCommentsPerReviewer(reviewerGroups, opts.MaxComments)
	}

	analysisPrompt := BuildAnalysisPrompt(reviewerGroups)

	analysis, err := anthropicClient.AnalyzeReviews(ctx, analysisPrompt, opts.AnalyzeModel)
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
	streams.ErrPrintf("  Analysis written to %s\n", paths.AnalysisPath)

	// --- Step 3: Generate review prompt ---
	streams.ErrPrintln("Step 3/3: Generating PR Review Prompt")

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
	streams.ErrPrintf("  Prompt written to %s\n", paths.PromptPath)

	return nil
}
