package fakes

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/CircleCI-Public/chunk-cli/acceptance/testutil"
)

// FakeGitHub serves canned responses for GitHub's GraphQL API.
type FakeGitHub struct {
	http.Handler
	Recorder *testutil.RequestRecorder

	mu             sync.RWMutex
	orgValidation  string
	orgRepos       string
	reviewActivity map[string]string // keyed by repo name
	errorRepos     map[string]string // keyed by repo name, value is full JSON error response
	rateLimit      string
}

func NewFakeGitHub() *FakeGitHub {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	rec := testutil.NewRecorder()
	f := &FakeGitHub{
		Handler:        r,
		Recorder:       rec,
		reviewActivity: map[string]string{},
		errorRepos:     map[string]string{},
		rateLimit:      `{"remaining": 4999, "resetAt": "2099-01-01T00:00:00Z"}`,
	}

	r.Use(rec.GinMiddleware())
	r.POST("/graphql", f.handleGraphQL)
	return f
}

func (f *FakeGitHub) SetOrgValidation(resp string)               { f.set(func() { f.orgValidation = resp }) }
func (f *FakeGitHub) SetOrgRepos(resp string)                    { f.set(func() { f.orgRepos = resp }) }
func (f *FakeGitHub) SetReviewActivity(repo string, resp string) { f.set(func() { f.reviewActivity[repo] = resp }) }
func (f *FakeGitHub) SetRateLimit(resp string)                   { f.set(func() { f.rateLimit = resp }) }
func (f *FakeGitHub) SetRepoError(repo string, resp string)      { f.set(func() { f.errorRepos[repo] = resp }) }

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
