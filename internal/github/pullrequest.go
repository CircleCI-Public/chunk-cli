package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

// NewPullRequest is a pull request to open.
type NewPullRequest struct {
	// Base is the branch the change is for and Head the branch that has it.
	// Both must exist on the repository.
	Base, Head string
	Title      string
	Body       string
	Draft      bool
}

// PullRequest is a pull request that was opened.
type PullRequest struct {
	Number int
	URL    string
}

// CreatePullRequest opens a pull request on owner/repo. A refusal carries the
// reason GitHub gave, such as a pull request for the branch already existing,
// and one for a token that cannot open pull requests says what it lacks.
func (c *Client) CreatePullRequest(ctx context.Context, owner, repo string, in NewPullRequest) (*PullRequest, error) {
	body := map[string]any{
		"title": in.Title,
		"head":  in.Head,
		"base":  in.Base,
		"body":  in.Body,
		"draft": in.Draft,
	}
	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	req := hc.NewRequest(http.MethodPost, "/repos/"+owner+"/"+repo+"/pulls",
		hc.Body(body), hc.JSONDecoder(&out))
	if _, err := c.http.Call(ctx, req); err != nil {
		return nil, pullRequestErr("open pull request", owner+"/"+repo, err)
	}
	if out.HTMLURL == "" {
		return nil, fmt.Errorf("open pull request: GitHub answered without the pull request's address")
	}
	return &PullRequest{Number: out.Number, URL: out.HTMLURL}, nil
}

// DefaultBranch is the branch owner/repo's pull requests target by default.
func (c *Client) DefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	req := hc.NewRequest(http.MethodGet, "/repos/"+owner+"/"+repo, hc.JSONDecoder(&out))
	if _, err := c.http.Call(ctx, req); err != nil {
		return "", pullRequestErr("look up the default branch", owner+"/"+repo, err)
	}
	if out.DefaultBranch == "" {
		return "", fmt.Errorf("look up the default branch: GitHub did not name one for %s/%s", owner, repo)
	}
	return out.DefaultBranch, nil
}

// pullRequestErr turns a refused request into a StatusError that says what
// GitHub said, and for the refusals a token causes, what to do about it.
func pullRequestErr(op, repo string, err error) error {
	var he *hc.HTTPError
	if !errors.As(err, &he) {
		return fmt.Errorf("%s: %w", op, err)
	}
	msg := serverMessage(he.Body)
	switch he.StatusCode {
	case http.StatusUnauthorized:
		msg = "the GitHub token was not accepted"
	case http.StatusForbidden, http.StatusNotFound:
		// GitHub answers 404 for a repository a token cannot see, and 403 or 404
		// for one it can read but not write.
		msg = fmt.Sprintf("the GitHub token has no access to %s, or not enough to open a pull request there "+
			"(it needs write access to pull requests: the repo scope, or a fine-grained token's Pull requests: write); "+
			"GitHub said: %s", repo, orNothing(msg))
	}
	return &StatusError{Op: op, StatusCode: he.StatusCode, ServerMessage: msg}
}

func orNothing(s string) string {
	if s == "" {
		return "nothing"
	}
	return s
}

// serverMessage reads the explanation out of a GitHub error body, joining the
// reasons it lists for a validation failure: {"message": "Validation Failed",
// "errors": [{"message": "A pull request already exists for ..."}]}.
func serverMessage(body []byte) string {
	var parsed struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	parts := []string{parsed.Message}
	for _, e := range parsed.Errors {
		if e.Message != "" {
			parts = append(parts, e.Message)
		}
	}
	return strings.Join(parts, ": ")
}
