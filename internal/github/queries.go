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

// branchPRQuery fetches the open PR for a given branch with check status and comments.
const branchPRQuery = `
  query BranchPR($owner: String!, $repo: String!, $branch: String!) {
    repository(owner: $owner, name: $repo) {
      pullRequests(first: 1, headRefName: $branch, states: [OPEN]) {
        nodes {
          number
          title
          url
          updatedAt
          reviews(last: 20) {
            nodes {
              state
            }
          }
          reviewThreads(first: 30) {
            nodes {
              isResolved
              comments(first: 1) {
                nodes {
                  author { login }
                  body
                  createdAt
                }
              }
            }
          }
          commits(last: 1) {
            nodes {
              commit {
                statusCheckRollup {
                  state
                  contexts(first: 50) {
                    nodes {
                      __typename
                      ... on CheckRun {
                        name
                        status
                        conclusion
                      }
                      ... on StatusContext {
                        context
                        state
                      }
                    }
                  }
                }
              }
            }
          }
        }
      }
    }
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
