package github

// PageInfo holds GraphQL pagination info.
type PageInfo struct {
	HasNextPage bool    `json:"hasNextPage"`
	EndCursor   *string `json:"endCursor"`
}

// RateLimit holds GitHub GraphQL rate limit info.
type RateLimit struct {
	Remaining int    `json:"remaining"`
	ResetAt   string `json:"resetAt"`
}

// RepoNode is a single repository from the org repos query.
type RepoNode struct {
	Name string `json:"name"`
}

// OrgReposData is the unwrapped data from the org repos query.
type OrgReposData struct {
	Organization struct {
		Repositories struct {
			PageInfo PageInfo   `json:"pageInfo"`
			Nodes    []RepoNode `json:"nodes"`
		} `json:"repositories"`
	} `json:"organization"`
	RateLimit RateLimit `json:"rateLimit"`
}

// ReviewNode is a single review on a PR.
type ReviewNode struct {
	Author    *Author `json:"author"`
	State     string  `json:"state"`
	CreatedAt string  `json:"createdAt"`
}

// CommentNode is a single review thread comment.
type CommentNode struct {
	Author    *Author `json:"author"`
	Body      string  `json:"body"`
	DiffHunk  string  `json:"diffHunk"`
	CreatedAt string  `json:"createdAt"`
}

// Author holds a GitHub user login.
type Author struct {
	Login string `json:"login"`
}

// PRNode is a single pull request from the review activity query.
type PRNode struct {
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	State     string  `json:"state"`
	UpdatedAt string  `json:"updatedAt"`
	Author    *Author `json:"author"`
	Reviews   struct {
		Nodes []ReviewNode `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads struct {
		Nodes []struct {
			Comments struct {
				Nodes []CommentNode `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
}

// RepoPRsData is the unwrapped data from the review activity query.
type RepoPRsData struct {
	Repository *struct {
		PullRequests struct {
			PageInfo PageInfo `json:"pageInfo"`
			Nodes    []PRNode `json:"nodes"`
		} `json:"pullRequests"`
	} `json:"repository"`
	RateLimit RateLimit `json:"rateLimit"`
}

// graphQLResponse wraps the top-level {"data": ...} envelope.
type graphQLResponse[T any] struct {
	Data   *T             `json:"data"`
	Errors []graphQLError `json:"errors"`
}

type graphQLError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
