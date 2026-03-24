package fixtures

// OrgValidationResponse returns a successful org validation response.
func OrgValidationResponse(org string) string {
	return `{
		"data": {
			"organization": {"login": "` + org + `"}
		}
	}`
}

// RateLimitResponse returns a healthy rate limit response.
func RateLimitResponse() string {
	return `{
		"data": {
			"rateLimit": {"remaining": 4999, "resetAt": "2099-01-01T00:00:00Z"}
		}
	}`
}

// OrgReposResponse returns a single-page repos response.
func OrgReposResponse(repoNames ...string) string {
	nodes := ""
	for i, name := range repoNames {
		if i > 0 {
			nodes += ","
		}
		nodes += `{"name": "` + name + `"}`
	}
	return `{
		"data": {
			"organization": {
				"repositories": {
					"pageInfo": {"hasNextPage": false, "endCursor": null},
					"nodes": [` + nodes + `]
				}
			},
			"rateLimit": {"remaining": 4998, "resetAt": "2099-01-01T00:00:00Z"}
		}
	}`
}

// ReviewActivityResponse returns a response with one PR containing review comments.
func ReviewActivityResponse() string {
	return `{
		"data": {
			"repository": {
				"pullRequests": {
					"pageInfo": {"hasNextPage": false, "endCursor": null},
					"nodes": [{
						"number": 42,
						"title": "Add feature X",
						"url": "https://github.com/test-org/test-repo/pull/42",
						"state": "MERGED",
						"updatedAt": "2026-03-01T00:00:00Z",
						"author": {"login": "pr-author"},
						"reviews": {
							"nodes": [
								{"author": {"login": "reviewer-alice"}, "state": "APPROVED", "createdAt": "2026-03-01T01:00:00Z"},
								{"author": {"login": "reviewer-bob"}, "state": "CHANGES_REQUESTED", "createdAt": "2026-03-01T02:00:00Z"}
							]
						},
						"reviewThreads": {
							"nodes": [
								{
									"comments": {
										"nodes": [
											{
												"author": {"login": "reviewer-alice"},
												"body": "Consider using early return here to reduce nesting",
												"diffHunk": "@@ -1,3 +1,5 @@\n func foo() {\n+  if err != nil {\n+    return err\n+  }",
												"createdAt": "2026-03-01T01:00:00Z"
											}
										]
									}
								},
								{
									"comments": {
										"nodes": [
											{
												"author": {"login": "reviewer-bob"},
												"body": "This needs error handling for the nil case",
												"diffHunk": "@@ -10,3 +12,5 @@\n resp, err := client.Do(req)",
												"createdAt": "2026-03-01T02:00:00Z"
											}
										]
									}
								},
								{
									"comments": {
										"nodes": [
											{
												"author": {"login": "reviewer-alice"},
												"body": "Prefer const over let for immutable bindings",
												"diffHunk": "@@ -20,1 +22,1 @@\n-let x = 5;\n+const x = 5;",
												"createdAt": "2026-03-01T03:00:00Z"
											}
										]
									}
								}
							]
						}
					}]
				}
			},
			"rateLimit": {"remaining": 4997, "resetAt": "2099-01-01T00:00:00Z"}
		}
	}`
}

// MultiReviewerResponse returns a response with 2 PRs, 3 human reviewers
// (alice: 3 comments, bob: 2, charlie: 1) plus dependabot[bot] in both
// reviews and comments. Supports testing --top N filtering, bot filtering
// on reviews, totalComments, and CSV ranking order.
func MultiReviewerResponse() string {
	return `{
		"data": {
			"repository": {
				"pullRequests": {
					"pageInfo": {"hasNextPage": false, "endCursor": null},
					"nodes": [
						{
							"number": 100,
							"title": "Big refactor",
							"url": "https://github.com/test-org/test-repo/pull/100",
							"state": "MERGED",
							"updatedAt": "2026-03-01T00:00:00Z",
							"author": {"login": "pr-author"},
							"reviews": {
								"nodes": [
									{"author": {"login": "reviewer-alice"}, "state": "APPROVED", "createdAt": "2026-03-01T01:00:00Z"},
									{"author": {"login": "reviewer-bob"}, "state": "CHANGES_REQUESTED", "createdAt": "2026-03-01T02:00:00Z"},
									{"author": {"login": "reviewer-charlie"}, "state": "APPROVED", "createdAt": "2026-03-01T03:00:00Z"},
									{"author": {"login": "dependabot[bot]"}, "state": "APPROVED", "createdAt": "2026-03-01T04:00:00Z"}
								]
							},
							"reviewThreads": {
								"nodes": [
									{"comments": {"nodes": [{"author": {"login": "reviewer-alice"}, "body": "Use early return", "diffHunk": "@@ -1,3 +1,5 @@\n+if err != nil {", "createdAt": "2026-03-01T01:00:00Z"}]}},
									{"comments": {"nodes": [{"author": {"login": "reviewer-alice"}, "body": "Prefer const", "diffHunk": "@@ -10,1 +10,1 @@\n-let x = 5;", "createdAt": "2026-03-01T01:30:00Z"}]}},
									{"comments": {"nodes": [{"author": {"login": "reviewer-bob"}, "body": "Handle nil case", "diffHunk": "@@ -20,3 +22,5 @@\n resp, err := client.Do(req)", "createdAt": "2026-03-01T02:00:00Z"}]}},
									{"comments": {"nodes": [{"author": {"login": "reviewer-charlie"}, "body": "Add docs", "diffHunk": "@@ -30,1 +32,1 @@\n+// TODO", "createdAt": "2026-03-01T03:00:00Z"}]}},
									{"comments": {"nodes": [{"author": {"login": "dependabot[bot]"}, "body": "Dep update safe", "diffHunk": "@@ -1,1 +1,1 @@\n-v1.0\n+v1.1", "createdAt": "2026-03-01T04:00:00Z"}]}}
								]
							}
						},
						{
							"number": 101,
							"title": "Small fix",
							"url": "https://github.com/test-org/test-repo/pull/101",
							"state": "MERGED",
							"updatedAt": "2026-03-02T00:00:00Z",
							"author": {"login": "pr-author"},
							"reviews": {
								"nodes": [
									{"author": {"login": "reviewer-alice"}, "state": "APPROVED", "createdAt": "2026-03-02T01:00:00Z"},
									{"author": {"login": "reviewer-bob"}, "state": "APPROVED", "createdAt": "2026-03-02T02:00:00Z"}
								]
							},
							"reviewThreads": {
								"nodes": [
									{"comments": {"nodes": [{"author": {"login": "reviewer-alice"}, "body": "LGTM with nit", "diffHunk": "@@ -5,1 +5,1 @@\n-old\n+new", "createdAt": "2026-03-02T01:00:00Z"}]}},
									{"comments": {"nodes": [{"author": {"login": "reviewer-bob"}, "body": "Typo here", "diffHunk": "@@ -8,1 +8,1 @@\n-teh\n+the", "createdAt": "2026-03-02T02:00:00Z"}]}}
								]
							}
						}
					]
				}
			},
			"rateLimit": {"remaining": 4997, "resetAt": "2099-01-01T00:00:00Z"}
		}
	}`
}

// RepoNotFoundError returns a GraphQL error response for a repository
// that cannot be resolved, used to test graceful error handling.
func RepoNotFoundError(org, repo string) string {
	return `{
		"data": null,
		"errors": [{"type": "NOT_FOUND", "message": "Could not resolve to a Repository with the name '` + org + `/` + repo + `'."}]
	}`
}

// ReviewActivityWithBotResponse includes a bot reviewer and bot commenter
// alongside human reviewers, for testing bot filtering.
func ReviewActivityWithBotResponse() string {
	return `{
		"data": {
			"repository": {
				"pullRequests": {
					"pageInfo": {"hasNextPage": false, "endCursor": null},
					"nodes": [{
						"number": 42,
						"title": "Add feature X",
						"url": "https://github.com/test-org/test-repo/pull/42",
						"state": "MERGED",
						"updatedAt": "2026-03-01T00:00:00Z",
						"author": {"login": "pr-author"},
						"reviews": {
							"nodes": [
								{"author": {"login": "reviewer-alice"}, "state": "APPROVED", "createdAt": "2026-03-01T01:00:00Z"},
								{"author": {"login": "reviewer-bob"}, "state": "CHANGES_REQUESTED", "createdAt": "2026-03-01T02:00:00Z"},
								{"author": {"login": "dependabot[bot]"}, "state": "APPROVED", "createdAt": "2026-03-01T03:00:00Z"}
							]
						},
						"reviewThreads": {
							"nodes": [
								{
									"comments": {
										"nodes": [
											{
												"author": {"login": "reviewer-alice"},
												"body": "Consider using early return here to reduce nesting",
												"diffHunk": "@@ -1,3 +1,5 @@\n func foo() {\n+  if err != nil {\n+    return err\n+  }",
												"createdAt": "2026-03-01T01:00:00Z"
											}
										]
									}
								},
								{
									"comments": {
										"nodes": [
											{
												"author": {"login": "reviewer-bob"},
												"body": "This needs error handling for the nil case",
												"diffHunk": "@@ -10,3 +12,5 @@\n resp, err := client.Do(req)",
												"createdAt": "2026-03-01T02:00:00Z"
											}
										]
									}
								},
								{
									"comments": {
										"nodes": [
											{
												"author": {"login": "dependabot[bot]"},
												"body": "This dependency update is safe to merge",
												"diffHunk": "@@ -1,1 +1,1 @@\n-dep: v1.0.0\n+dep: v1.1.0",
												"createdAt": "2026-03-01T03:00:00Z"
											}
										]
									}
								},
								{
									"comments": {
										"nodes": [
											{
												"author": {"login": "reviewer-alice"},
												"body": "Prefer const over let for immutable bindings",
												"diffHunk": "@@ -20,1 +22,1 @@\n-let x = 5;\n+const x = 5;",
												"createdAt": "2026-03-01T03:00:00Z"
											}
										]
									}
								}
							]
						}
					}]
				}
			},
			"rateLimit": {"remaining": 4997, "resetAt": "2099-01-01T00:00:00Z"}
		}
	}`
}
