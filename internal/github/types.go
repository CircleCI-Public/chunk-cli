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

// Author holds a GitHub user login.
type Author struct {
	Login string `json:"login"`
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
