package buildprompt

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/closer"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
)

// ResolveOrgAndRepos resolves the org and repos from flags or git remote.
func ResolveOrgAndRepos(org string, repos string, workDir string) (string, []string, error) {
	repoList := splitRepos(repos)

	if org != "" && len(repoList) == 0 {
		return "", nil, fmt.Errorf("--repos is required when --org is provided. Omit --org to auto-detect from git remote")
	}

	if org != "" {
		return org, repoList, nil
	}

	detectedOrg, detectedRepo, err := gitremote.DetectOrgAndRepo(workDir)
	if err != nil {
		return "", nil, fmt.Errorf("auto-detect org from git remote: %w", err)
	}

	if len(repoList) > 0 {
		return detectedOrg, repoList, nil
	}
	return detectedOrg, []string{detectedRepo}, nil
}

func splitRepos(repos string) []string {
	if repos == "" {
		return nil
	}
	var result []string
	for _, r := range strings.Split(repos, ",") {
		r = strings.TrimSpace(r)
		if r != "" {
			result = append(result, r)
		}
	}
	return result
}

// DeriveOutputPaths computes the intermediate output file paths.
func DeriveOutputPaths(outputPath string) OutputPaths {
	base := strings.TrimSuffix(outputPath, ".md")
	return OutputPaths{
		PromptPath:   outputPath,
		DetailsPath:  base + "-details.json",
		AnalysisPath: base + "-analysis.md",
		CSVPath:      base + "-details-pr-rankings.csv",
	}
}

// AggregateActivity merges activity maps from multiple repos into a sorted slice.
func AggregateActivity(repoActivities []map[string]*github.UserActivity) []*github.UserActivity {
	merged := map[string]*github.UserActivity{}

	for _, repoActivity := range repoActivities {
		for login, activity := range repoActivity {
			existing, ok := merged[login]
			if !ok {
				// Clone
				clone := *activity
				clone.ReposActiveIn = map[string]bool{}
				for k, v := range activity.ReposActiveIn {
					clone.ReposActiveIn[k] = v
				}
				merged[login] = &clone
				continue
			}
			existing.TotalActivity += activity.TotalActivity
			existing.ReviewsGiven += activity.ReviewsGiven
			existing.Approvals += activity.Approvals
			existing.ChangesRequested += activity.ChangesRequested
			existing.ReviewComments += activity.ReviewComments
			for repo := range activity.ReposActiveIn {
				existing.ReposActiveIn[repo] = true
			}
		}
	}

	result := make([]*github.UserActivity, 0, len(merged))
	for _, a := range merged {
		result = append(result, a)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].TotalActivity > result[j].TotalActivity
	})
	return result
}

// TopN returns the top n activities. Returns an error if n <= 0.
func TopN(activities []*github.UserActivity, n int) ([]*github.UserActivity, error) {
	if n <= 0 {
		return nil, fmt.Errorf("top must be a positive integer, got %d", n)
	}
	if n >= len(activities) {
		return activities, nil
	}
	return activities[:n], nil
}

// AggregateDetails flattens detail slices from multiple repos.
func AggregateDetails(allDetails [][]github.ReviewCommentDetail) []github.ReviewCommentDetail {
	var result []github.ReviewCommentDetail
	for _, d := range allDetails {
		result = append(result, d...)
	}
	return result
}

// FilterDetailsByReviewers keeps only comments from the given reviewer logins.
func FilterDetailsByReviewers(details []github.ReviewCommentDetail, reviewers []*github.UserActivity) []github.ReviewCommentDetail {
	logins := map[string]bool{}
	for _, r := range reviewers {
		logins[r.Login] = true
	}
	var filtered []github.ReviewCommentDetail
	for _, d := range details {
		if logins[d.Reviewer] {
			filtered = append(filtered, d)
		}
	}
	return filtered
}

// WriteDetailsJSON writes the details JSON output file.
func WriteDetailsJSON(details []github.ReviewCommentDetail, path string, org string, since time.Time, totalRepos int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	output := DetailsJSON{
		Metadata: DetailsMetadata{
			Organization:  org,
			Since:         since.Format("2006-01-02"),
			AnalyzedAt:    time.Now().Format("2006-01-02"),
			TotalRepos:    totalRepos,
			TotalComments: len(details),
		},
		Comments: details,
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// AggregatePRRankings computes PR rankings sorted by total comments descending.
func AggregatePRRankings(details []github.ReviewCommentDetail) []PRRankingRow {
	type prAgg struct {
		repo      string
		number    int
		title     string
		author    string
		url       string
		state     string
		comments  int
		reviewers map[string]bool
	}

	prMap := map[string]*prAgg{}

	for _, d := range details {
		key := fmt.Sprintf("%s/%d", d.PR.Repo, d.PR.Number)
		existing, ok := prMap[key]
		if !ok {
			prMap[key] = &prAgg{
				repo:      d.PR.Repo,
				number:    d.PR.Number,
				title:     d.PR.Title,
				author:    d.PR.Author,
				url:       d.PR.URL,
				state:     d.PR.State,
				comments:  1,
				reviewers: map[string]bool{d.Reviewer: true},
			}
			continue
		}
		existing.comments++
		existing.reviewers[d.Reviewer] = true
	}

	rankings := make([]PRRankingRow, 0, len(prMap))
	for _, pr := range prMap {
		rankings = append(rankings, PRRankingRow{
			Repo:          pr.repo,
			PRNumber:      pr.number,
			PRTitle:       pr.title,
			PRAuthor:      pr.author,
			PRURL:         pr.url,
			TotalComments: pr.comments,
			ReviewerCount: len(pr.reviewers),
			State:         pr.state,
		})
	}

	sort.Slice(rankings, func(i, j int) bool {
		return rankings[i].TotalComments > rankings[j].TotalComments
	})

	for i := range rankings {
		rankings[i].Rank = i + 1
	}

	return rankings
}

// WritePRRankingsCSV writes the PR rankings CSV file.
func WritePRRankingsCSV(rankings []PRRankingRow, path string) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer closer.ErrorHandler(f, &err)

	w := csv.NewWriter(f)
	defer w.Flush()

	header := []string{"rank", "repo", "pr_number", "pr_title", "pr_author", "total_comments", "reviewer_count", "state", "pr_url"}
	if err := w.Write(header); err != nil {
		return err
	}

	for _, r := range rankings {
		row := []string{
			fmt.Sprintf("%d", r.Rank),
			r.Repo,
			fmt.Sprintf("%d", r.PRNumber),
			r.PRTitle,
			r.PRAuthor,
			fmt.Sprintf("%d", r.TotalComments),
			fmt.Sprintf("%d", r.ReviewerCount),
			r.State,
			r.PRURL,
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	return nil
}

// GroupByReviewer groups comments by reviewer login.
func GroupByReviewer(comments []github.ReviewCommentDetail) []ReviewerGroup {
	groups := map[string][]ReviewCommentWithContext{}

	for _, c := range comments {
		ctx := ReviewCommentWithContext{
			Reviewer:  c.Reviewer,
			Body:      c.Body,
			DiffHunk:  c.DiffHunk,
			CreatedAt: c.CreatedAt,
			Repo:      c.PR.Repo,
			PR: &PRContext{
				Number: c.PR.Number,
				Title:  c.PR.Title,
				Author: c.PR.Author,
				URL:    c.PR.URL,
				State:  c.PR.State,
			},
		}
		groups[c.Reviewer] = append(groups[c.Reviewer], ctx)
	}

	result := make([]ReviewerGroup, 0, len(groups))
	for reviewer, comments := range groups {
		result = append(result, ReviewerGroup{
			Reviewer:      reviewer,
			Comments:      comments,
			TotalComments: len(comments),
		})
	}
	return result
}

// LimitCommentsPerReviewer limits each reviewer group to the most recent N comments.
func LimitCommentsPerReviewer(groups []ReviewerGroup, maxComments int) []ReviewerGroup {
	if maxComments <= 0 {
		return groups
	}
	result := make([]ReviewerGroup, len(groups))
	for i, g := range groups {
		if len(g.Comments) <= maxComments {
			result[i] = g
			continue
		}
		sorted := make([]ReviewCommentWithContext, len(g.Comments))
		copy(sorted, g.Comments)
		sort.Slice(sorted, func(a, b int) bool {
			return sorted[a].CreatedAt > sorted[b].CreatedAt
		})
		limited := sorted[:maxComments]
		result[i] = ReviewerGroup{
			Reviewer:      g.Reviewer,
			Comments:      limited,
			TotalComments: len(limited),
		}
	}
	return result
}

// EstimateTokenCount estimates the token count for a prompt using ~4 chars per token.
func EstimateTokenCount(text string) int {
	return (len(text) + 3) / 4
}

// BuildAnalysisPrompt builds the prompt for Claude to analyze review patterns.
func BuildAnalysisPrompt(groups []ReviewerGroup) string {
	totalComments := 0
	reviewerNames := make([]string, 0, len(groups))
	for _, g := range groups {
		totalComments += g.TotalComments
		reviewerNames = append(reviewerNames, g.Reviewer)
	}

	reviewerData := formatReviewerData(groups)

	return fmt.Sprintf(`You are analyzing code review feedback from senior engineers at CircleCI.

# Context
You have %d review comments from %d reviewer(s) across multiple repositories. Your goal is to identify:
1. What patterns and practices each reviewer emphasizes
2. What key principles they're trying to teach
3. Recurring themes across their feedback

# Data
%s

# Instructions
Analyze the review comments and produce a structured report with these sections:

## 1. Per-Reviewer Analysis
For each reviewer (%s):

### Key Practices
Identify 3-7 patterns in their feedback. For each pattern:
- **Name**: Short, descriptive title
- **Description**: What principle/practice they're emphasizing
- **Examples**: 2-3 concrete examples with code context and quotes

Examples of patterns to look for:
- Observability/instrumentation preferences (like preferring specific o11y methods)
- Naming conventions (like "o11y" abbreviation, metric naming patterns)
- Code organization principles
- Testing approaches
- Performance considerations
- Architectural guidance
- Error handling patterns

### Notable Repos
Identify which repositories have particularly instructive feedback and why.

## 2. Cross-Cutting Themes
Identify 2-4 themes that appear across multiple reviewers or are especially important

## 3. Recommendations
Based on the patterns, what could be:
- Automated (linters, CI checks)
- Documented (style guides, architectural docs)
- Taught (onboarding, examples)

# Output Format
Use clear markdown with headers, bullet points, and code snippets where relevant.
Keep it concise but specific - use actual quotes from the comments.`, totalComments, len(groups), reviewerData, strings.Join(reviewerNames, ", "))
}

// formatReviewerData formats reviewer groups for the analysis prompt.
func formatReviewerData(groups []ReviewerGroup) string {
	var sb strings.Builder

	for _, g := range groups {
		fmt.Fprintf(&sb, "\n## %s (%d comments)\n\n", g.Reviewer, g.TotalComments)

		// Group by repo
		repoComments := map[string][]ReviewCommentWithContext{}
		for _, c := range g.Comments {
			repoComments[c.Repo] = append(repoComments[c.Repo], c)
		}

		for repo, comments := range repoComments {
			fmt.Fprintf(&sb, "### Repository: %s (%d comments)\n\n", repo, len(comments))
			for _, c := range comments {
				fmt.Fprintf(&sb, "> %s\n", c.Body)
				if c.PR != nil {
					fmt.Fprintf(&sb, "\n**PR**: [#%d](%s) - %s (by %s)\n", c.PR.Number, c.PR.URL, c.PR.Title, c.PR.Author)
				}
				if c.DiffHunk != "" {
					fmt.Fprintf(&sb, "\n**Code context:**\n```\n%s\n```\n", c.DiffHunk)
				}
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// FormatMarkdownReport formats the analysis into a markdown report.
func FormatMarkdownReport(analysis string, detailsPath string, totalComments int, reviewers []string) string {
	return fmt.Sprintf(`# Code Review Pattern Analysis

**Generated:** %s
**Source:** %s
**Total Comments:** %d
**Reviewers:** %s

---

%s

---

*This analysis was generated using Claude AI by analyzing code review patterns.*
`, time.Now().Format(time.RFC3339), detailsPath, totalComments, strings.Join(reviewers, ", "), analysis)
}
