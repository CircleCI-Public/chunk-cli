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
