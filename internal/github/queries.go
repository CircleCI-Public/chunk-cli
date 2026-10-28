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

// pullRequestProgressQuery fetches what a reviewer would look at on one pull
// request: whether it is merged, who has reviewed it, and how its checks stand.
const pullRequestProgressQuery = `
  query PullRequestProgress($owner: String!, $repo: String!, $number: Int!) {
    repository(owner: $owner, name: $repo) {
      pullRequest(number: $number) {
        state
        isDraft
        reviewRequests(first: 1) { totalCount }
        reviews(last: 20) {
          totalCount
          nodes { state author { login } }
        }
        reviewThreads(first: 50) {
          nodes { isResolved }
        }
        commits(last: 1) {
          nodes {
            commit {
              statusCheckRollup { state }
            }
          }
        }
      }
    }
  }
`
