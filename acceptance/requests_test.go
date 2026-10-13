package acceptance

import (
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/recorder"
)

func filterByPath(reqs []recorder.RecordedRequest, path string) []recorder.RecordedRequest {
	var filtered []recorder.RecordedRequest
	for _, r := range reqs {
		if r.URL.Path == path {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func filterByPathPrefix(reqs []recorder.RecordedRequest, prefix string) []recorder.RecordedRequest {
	var filtered []recorder.RecordedRequest
	for _, r := range reqs {
		if strings.HasPrefix(r.URL.Path, prefix) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}
