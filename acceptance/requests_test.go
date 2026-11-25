package acceptance

import (
	"encoding/json"
	"net/http"
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

// createdNamesWithPrefix returns the name of each created sidecar whose name
// starts with prefix.
func createdNamesWithPrefix(reqs []recorder.RecordedRequest, prefix string) []string {
	var names []string
	for _, r := range reqs {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/sidecar/instances" {
			continue
		}
		var body struct {
			Data struct {
				Attributes struct {
					Name string `json:"name"`
				} `json:"attributes"`
			} `json:"data"`
		}
		if err := json.Unmarshal(r.Body, &body); err != nil {
			continue
		}
		if name := body.Data.Attributes.Name; strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names
}
