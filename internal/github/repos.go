package github

import (
	"context"
	"fmt"
	"strings"
)

// FetchOrgRepos returns repository names for the given org.
// If filterRepos is non-empty, those names are returned directly without querying.
func (c *Client) FetchOrgRepos(ctx context.Context, org string, filterRepos []string) ([]string, error) {
	if len(filterRepos) > 0 {
		return filterRepos, nil
	}

	var repos []string
	var cursor *string

	for {
		vars := map[string]any{"org": org}
		if cursor != nil {
			vars["cursor"] = *cursor
		}

		var resp graphQLResponse[OrgReposData]
		if err := c.do(ctx, orgReposQuery, vars, &resp); err != nil {
			return nil, fmt.Errorf("fetch org repos: %w", err)
		}
		if hasResolutionError(resp.Errors) {
			return nil, fmt.Errorf("Could not resolve organization %q", org)
		}
		if resp.Data == nil {
			return nil, fmt.Errorf("no data in org repos response")
		}

		repoData := resp.Data.Organization.Repositories
		for _, node := range repoData.Nodes {
			repos = append(repos, node.Name)
		}

		if !repoData.PageInfo.HasNextPage {
			break
		}
		cursor = repoData.PageInfo.EndCursor
	}

	return repos, nil
}

func hasResolutionError(errs []graphQLError) bool {
	for _, e := range errs {
		if strings.Contains(e.Message, "Could not resolve") {
			return true
		}
	}
	return false
}
