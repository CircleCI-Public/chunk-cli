package fakes

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/recorder"
)

// FakeGitHub serves canned responses for GitHub's GraphQL API.
type FakeGitHub struct {
	http.Handler
	Recorder *recorder.RequestRecorder

	mu             sync.RWMutex
	orgValidation  string
	orgRepos       string
	reviewActivity map[string]string // keyed by repo name
	errorRepos     map[string]string // keyed by repo name, value is full JSON error response
	rateLimit      string

	defaultBranch string
	pullRefusal   *pullRefusal
	pulls         []OpenedPullRequest
}

type pullRefusal struct {
	status int
	body   string
}

// OpenedPullRequest is a pull request the fake was asked to open.
type OpenedPullRequest struct {
	Owner, Repo string
	Number      int
	Title, Body string
	Head, Base  string
	Draft       bool
}

func NewFakeGitHub() *FakeGitHub {
	r, rec := newRouter()
	f := &FakeGitHub{
		Handler:        r,
		Recorder:       rec,
		reviewActivity: map[string]string{},
		errorRepos:     map[string]string{},
		rateLimit:      `{"remaining": 4999, "resetAt": "2099-01-01T00:00:00Z"}`,
		defaultBranch:  "main",
	}

	r.POST("/graphql", f.handleGraphQL)
	r.GET("/user", f.handleUser)
	r.GET("/repos/:owner/:repo", f.handleRepo)
	r.POST("/repos/:owner/:repo/pulls", f.handleCreatePull)
	r.GET("/repos/:owner/:repo/pulls", f.handleListPulls)
	return f
}

// SetDefaultBranch sets the default branch GET /repos/{owner}/{repo} reports.
func (f *FakeGitHub) SetDefaultBranch(name string) { f.set(func() { f.defaultBranch = name }) }

// RefusePullRequests makes opening a pull request fail with status and the
// given JSON body, as GitHub does for a token without access or a duplicate.
func (f *FakeGitHub) RefusePullRequests(status int, body string) {
	f.set(func() { f.pullRefusal = &pullRefusal{status: status, body: body} })
}

// AddPullRequest records a pull request as already open for head, as one the
// branch was given before this run pushed to it again.
func (f *FakeGitHub) AddPullRequest(owner, repo, head string) OpenedPullRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := OpenedPullRequest{Owner: owner, Repo: repo, Number: len(f.pulls) + 1, Head: head}
	f.pulls = append(f.pulls, p)
	return p
}

// PullRequests are the pull requests opened so far, oldest first.
func (f *FakeGitHub) PullRequests() []OpenedPullRequest {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]OpenedPullRequest(nil), f.pulls...)
}

func (f *FakeGitHub) handleRepo(c *gin.Context) {
	if c.GetHeader("authorization") == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Bad credentials"})
		return
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	c.JSON(http.StatusOK, gin.H{"default_branch": f.defaultBranch})
}

func (f *FakeGitHub) handleCreatePull(c *gin.Context) {
	if c.GetHeader("authorization") == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Bad credentials"})
		return
	}
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Draft bool   `json:"draft"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Problems parsing JSON"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pullRefusal != nil {
		c.Data(f.pullRefusal.status, "application/json", []byte(f.pullRefusal.body))
		return
	}
	owner, repo := c.Param("owner"), c.Param("repo")
	number := len(f.pulls) + 1
	f.pulls = append(f.pulls, OpenedPullRequest{
		Owner: owner, Repo: repo, Number: number,
		Title: in.Title, Body: in.Body, Head: in.Head, Base: in.Base, Draft: in.Draft,
	})
	c.JSON(http.StatusCreated, gin.H{
		"number":   number,
		"html_url": "https://github.com/" + owner + "/" + repo + "/pull/" + strconv.Itoa(number),
	})
}

// handleListPulls answers GET /repos/{owner}/{repo}/pulls?head={owner}:{branch}
// with the pull requests opened for that branch, as GitHub does.
func (f *FakeGitHub) handleListPulls(c *gin.Context) {
	if c.GetHeader("authorization") == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Bad credentials"})
		return
	}
	owner, repo := c.Param("owner"), c.Param("repo")
	head := strings.TrimPrefix(c.Query("head"), owner+":")
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := []gin.H{}
	for _, p := range f.pulls {
		if p.Owner != owner || p.Repo != repo || (head != "" && p.Head != head) {
			continue
		}
		out = append(out, gin.H{
			"number":   p.Number,
			"html_url": "https://github.com/" + owner + "/" + repo + "/pull/" + strconv.Itoa(p.Number),
		})
	}
	c.JSON(http.StatusOK, out)
}

func (f *FakeGitHub) handleUser(c *gin.Context) {
	if c.GetHeader("authorization") == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Bad credentials"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"login": "fake-user"})
}

func (f *FakeGitHub) SetOrgValidation(resp string) { f.set(func() { f.orgValidation = resp }) }
func (f *FakeGitHub) SetOrgRepos(resp string)      { f.set(func() { f.orgRepos = resp }) }
func (f *FakeGitHub) SetReviewActivity(repo string, resp string) {
	f.set(func() { f.reviewActivity[repo] = resp })
}
func (f *FakeGitHub) SetRateLimit(resp string) { f.set(func() { f.rateLimit = resp }) }
func (f *FakeGitHub) SetRepoError(repo string, resp string) {
	f.set(func() { f.errorRepos[repo] = resp })
}

func (f *FakeGitHub) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *FakeGitHub) handleGraphQL(c *gin.Context) {
	auth := c.GetHeader("authorization")
	if auth == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Bad credentials"})
		return
	}

	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid json"})
		return
	}

	f.mu.RLock()
	defer f.mu.RUnlock()

	query := body.Query

	switch {
	// OrgRepos query contains both "organization(login:" and "repositories(" — match it first
	case strings.Contains(query, "repositories("):
		if f.orgRepos != "" {
			c.Data(http.StatusOK, "application/json", []byte(f.orgRepos))
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"organization": gin.H{
					"repositories": gin.H{
						"pageInfo": gin.H{"hasNextPage": false, "endCursor": nil},
						"nodes":    []any{},
					},
				},
				"rateLimit": json.RawMessage(f.rateLimit),
			},
		})

	case strings.Contains(query, "organization(login:"):
		if f.orgValidation != "" {
			c.Data(http.StatusOK, "application/json", []byte(f.orgValidation))
			return
		}
		org, _ := body.Variables["org"].(string)
		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"organization": gin.H{"login": org},
			},
		})

	case strings.Contains(query, "pullRequests("):
		repo, _ := body.Variables["repo"].(string)
		if errResp, ok := f.errorRepos[repo]; ok {
			c.Data(http.StatusOK, "application/json", []byte(errResp))
			return
		}
		if resp, ok := f.reviewActivity[repo]; ok {
			c.Data(http.StatusOK, "application/json", []byte(resp))
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"repository": gin.H{
					"pullRequests": gin.H{
						"pageInfo": gin.H{"hasNextPage": false, "endCursor": nil},
						"nodes":    []any{},
					},
				},
				"rateLimit": json.RawMessage(f.rateLimit),
			},
		})

	case strings.Contains(query, "rateLimit"):
		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"rateLimit": json.RawMessage(f.rateLimit),
			},
		})

	default:
		c.JSON(http.StatusBadRequest, gin.H{"errors": []gin.H{{"message": "unrecognized query"}}})
	}
}
