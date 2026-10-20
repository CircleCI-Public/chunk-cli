package server

import (
	"context"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
)

// prTrackFor is how long after a run ends its pull request is still followed.
// It matches how long `chunk watch` keeps an ended run in its list: a merge
// after that would have nowhere to show.
const prTrackFor = 24 * time.Hour

// prProgress follows the pull requests that runs opened, so a run can say
// whether its pull request is waiting for a review, has feedback to deal with
// or was merged. Where prMonitor follows the open pull request of the branch a
// project has checked out, this follows each run's own, by number, and sees
// merged and closed ones too. It is advisory like prMonitor: failures are
// logged and ignored.
type prProgress struct {
	client *github.Client // nil when no GitHub credentials

	mu        sync.Mutex
	lastFetch map[*sessionEntry]time.Time
	inflight  map[*sessionEntry]bool
}

func newPRProgress(client *github.Client) *prProgress {
	return &prProgress{
		client:    client,
		lastFetch: make(map[*sessionEntry]time.Time),
		inflight:  make(map[*sessionEntry]bool),
	}
}

// refresh starts a background lookup for each followed pull request that is
// due one, at most one per PRPollInterval. It must not block.
func (p *prProgress) refresh(ctx context.Context, store *sessionStore) {
	if p == nil || p.client == nil {
		return
	}
	entries := store.entries()
	p.prune(entries)
	for _, e := range entries {
		owner, repo, number, ok := e.trackedPullRequest(time.Now())
		if !ok {
			continue
		}
		p.mu.Lock()
		due := !p.inflight[e] && time.Since(p.lastFetch[e]) >= PRPollInterval
		if due {
			p.inflight[e] = true
		}
		p.mu.Unlock()
		if !due {
			continue
		}
		go p.fetch(ctx, e, owner, repo, number)
	}
}

// prune forgets the sessions the store has since evicted.
func (p *prProgress) prune(live []*sessionEntry) {
	keep := make(map[*sessionEntry]bool, len(live))
	for _, e := range live {
		keep[e] = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for e := range p.lastFetch {
		if !keep[e] {
			delete(p.lastFetch, e)
		}
	}
}

func (p *prProgress) fetch(ctx context.Context, e *sessionEntry, owner, repo string, number int) {
	// A stalled GitHub request must not hold the slot open indefinitely.
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	info, err := p.client.FetchPRProgress(fctx, owner, repo, number)

	// The attempt is recorded either way so a failure backs off rather than
	// hammering the API.
	p.mu.Lock()
	delete(p.inflight, e)
	p.lastFetch[e] = time.Now()
	p.mu.Unlock()

	if err != nil {
		log.Printf("chunkd: PR progress for %s/%s#%d: %v", owner, repo, number, err)
		return
	}
	if info == nil {
		return
	}
	status := prStatus(info)
	e.setPullRequest(func(pr *chunkd.PullRequest) { pr.Status = status })
}

// prStatus is where a pull request stands, as a chunkd.PRStatus constant. What
// needs the author's attention outranks what is only waiting: a pull request
// with changes requested is feedback however its checks are doing.
func prStatus(info *github.PRProgress) string {
	switch info.State {
	case "MERGED":
		return chunkd.PRStatusMerged
	case "CLOSED":
		return chunkd.PRStatusClosed
	}
	switch {
	case info.ChangesRequested || info.OpenThreads > 0:
		return chunkd.PRStatusFeedback
	case info.CheckState == "FAILURE" || info.CheckState == "ERROR":
		return chunkd.PRStatusChecksFailing
	case !info.Draft && (info.ReviewRequested || info.Reviewed):
		return chunkd.PRStatusInReview
	}
	return chunkd.PRStatusUp
}

// trackedPullRequest names the pull request of the session if it is worth
// asking GitHub about now: it is open, has not settled, and its run is either
// going or ended within prTrackFor.
func (e *sessionEntry) trackedPullRequest(now time.Time) (owner, repo string, number int, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.s.Factory == nil {
		return "", "", 0, false
	}
	pr := e.s.Factory.PullRequest
	if !pr.Opened() || pr.Settled() || pr.Number == 0 {
		return "", "", 0, false
	}
	if e.s.EndedAt != nil && now.Sub(*e.s.EndedAt) > prTrackFor {
		return "", "", 0, false
	}
	owner, repo, ok = ownerRepoOf(pr.URL)
	return owner, repo, pr.Number, ok
}

// ownerRepoOf reads the repository out of a pull request's address, which
// ends in /{owner}/{repo}/pull/{number}.
func ownerRepoOf(raw string) (owner, repo string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	n := len(parts)
	if n < 4 || parts[n-2] != "pull" {
		return "", "", false
	}
	if _, err := strconv.Atoi(parts[n-1]); err != nil {
		return "", "", false
	}
	return parts[n-4], parts[n-3], true
}
