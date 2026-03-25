package github

const orgReposQuery = `
  query OrgRepos($org: String!, $cursor: String) {
    organization(login: $org) {
      repositories(first: 100, after: $cursor, isArchived: false) {
        pageInfo { hasNextPage endCursor }
        nodes { name }
      }
    }
    rateLimit { remaining resetAt }
  }
`

const reviewActivityQuery = `
  query RepoReviewActivity($org: String!, $repo: String!, $cursor: String) {
    repository(owner: $org, name: $repo) {
      pullRequests(first: 20, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC}) {
        pageInfo { hasNextPage endCursor }
        nodes {
          number
          title
          url
          state
          updatedAt
          author { login }
          reviews(first: 50) {
            nodes {
              author { login }
              state
              createdAt
            }
          }
          reviewThreads(first: 100) {
            nodes {
              comments(first: 100) {
                nodes {
                  author { login }
                  body
                  diffHunk
                  createdAt
                }
              }
            }
          }
        }
      }
    }
    rateLimit { remaining resetAt }
  }
`
