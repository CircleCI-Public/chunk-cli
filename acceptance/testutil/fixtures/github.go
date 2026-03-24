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
