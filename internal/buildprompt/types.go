package buildprompt

import (
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/github"
)

// Options holds the configuration for a build-prompt run.
type Options struct {
	Org                string
	Repos              []string
	Top                int
	Since              time.Time
	OutputPath         string
	MaxComments        int
	AnalyzeModel       string
	PromptModel        string
	IncludeAttribution bool
}

// OutputPaths holds the derived file paths for build-prompt outputs.
type OutputPaths struct {
	PromptPath   string
	DetailsPath  string
	AnalysisPath string
	CSVPath      string
}

// ReviewerGroup groups comments by reviewer for analysis.
type ReviewerGroup struct {
	Reviewer      string
	Comments      []ReviewCommentWithContext
	TotalComments int
}

// ReviewCommentWithContext is a comment with repo context for analysis.
type ReviewCommentWithContext struct {
	Reviewer  string
	Body      string
	DiffHunk  string
	CreatedAt string
	Repo      string
	PR        *PRContext
}

// PRContext holds PR metadata for a comment.
type PRContext struct {
	Number int
	Title  string
	Author string
	URL    string
	State  string
}

// PRRankingRow is a row in the PR rankings CSV.
type PRRankingRow struct {
	Rank          int
	Repo          string
	PRNumber      int
	PRTitle       string
	PRAuthor      string
	PRURL         string
	TotalComments int
	ReviewerCount int
	State         string
}

// DetailsJSON is the structure of the details output file.
type DetailsJSON struct {
	Metadata DetailsMetadata              `json:"metadata"`
	Comments []github.ReviewCommentDetail `json:"comments"`
}

// DetailsMetadata is the metadata section of the details JSON.
type DetailsMetadata struct {
	Organization  string `json:"organization"`
	Since         string `json:"since,omitempty"`
	AnalyzedAt    string `json:"analyzedAt"`
	TotalRepos    int    `json:"totalReposAnalyzed"`
	TotalComments int    `json:"totalComments"`
}
