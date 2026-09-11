package watchd

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/github"
)

// PRPollInterval is the minimum time between PR fetches for a given branch.
// GitHub's API is rate-limited, so polling too frequently is wasteful. A branch
// change resets the clock so the new branch's PR is fetched promptly.
const PRPollInterval = 60 * time.Second

// prProjectKey identifies a (project, branch) pair for fetch-tracking.
type prProjectKey struct {
	root   string
	branch string
}

// prMonitor fetches and caches the open PR state for each watched project's
// current branch. It is advisory: failures are logged and silently ignored so
// a missing or unauthenticated GitHub token never breaks the daemon.
type prMonitor struct {
	client *github.Client // nil when no GitHub credentials

	mu        sync.Mutex
	states    map[string]*PRState // latest PR state per project root (nil = no open PR)
	lastFetch map[prProjectKey]time.Time
	inflight  map[prProjectKey]bool
}

func newPRMonitor(client *github.Client) *prMonitor {
	return &prMonitor{
		client:    client,
		states:    make(map[string]*PRState),
		lastFetch: make(map[prProjectKey]time.Time),
		inflight:  make(map[prProjectKey]bool),
	}
}

// maybeRefresh fires a background fetch for the project's branch if one is due.
// It is called from the daemon's poll path and must not block.
func (pm *prMonitor) maybeRefresh(ctx context.Context, root, branch, org, repo string) {
	if pm.client == nil || branch == "" || org == "" || repo == "" {
		return
	}
	key := prProjectKey{root: root, branch: branch}

	pm.mu.Lock()
	alreadyInflight := pm.inflight[key]
	lastFetch := pm.lastFetch[key]
	pm.mu.Unlock()

	if alreadyInflight || time.Since(lastFetch) < PRPollInterval {
		return
	}

	pm.mu.Lock()
	pm.inflight[key] = true
	pm.mu.Unlock()

	go func() {
		defer func() {
			pm.mu.Lock()
			delete(pm.inflight, key)
			pm.mu.Unlock()
		}()
		pm.fetch(ctx, root, branch, org, repo, key)
	}()
}

func (pm *prMonitor) fetch(ctx context.Context, root, branch, org, repo string, key prProjectKey) {
	// Use a bounded context: a stalled GitHub request should not hold the slot
	// open indefinitely.
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	info, err := pm.client.FetchPRForBranch(fctx, org, repo, branch)
	if err != nil {
		log.Printf("watchd: PR monitor for %s/%s@%s: %v", org, repo, branch, err)
		// Record the attempt time so we back off rather than hammer the API.
		pm.mu.Lock()
		pm.lastFetch[key] = time.Now()
		pm.mu.Unlock()
		return
	}

	var state *PRState
	if info != nil {
		state = convertPRInfo(info)
	}

	pm.mu.Lock()
	pm.states[root] = state
	pm.lastFetch[key] = time.Now()
	pm.mu.Unlock()
}

// annotate fills snap.PR from the cached PR state.
func (pm *prMonitor) annotate(snap *ProjectSnapshot) {
	if pm == nil {
		return
	}
	pm.mu.Lock()
	state, ok := pm.states[snap.Root]
	pm.mu.Unlock()
	if ok {
		snap.PR = state
	}
}

func convertPRInfo(info *github.PRStatusInfo) *PRState {
	state := &PRState{
		Number:           info.Number,
		Title:            info.Title,
		URL:              info.URL,
		UpdatedAt:        info.UpdatedAt,
		CheckState:       info.OverallCheckState,
		ChangesRequested: info.ChangesRequested,
		FetchedAt:        time.Now(),
	}
	for _, c := range info.Checks {
		state.Checks = append(state.Checks, PRCheck{
			Name:       c.Name,
			Status:     c.Status,
			Conclusion: c.Conclusion,
		})
	}
	for _, c := range info.Comments {
		state.Comments = append(state.Comments, PRComment{
			Author:    c.Author,
			Body:      c.Body,
			CreatedAt: c.CreatedAt,
			Resolved:  c.Resolved,
		})
	}
	return state
}
