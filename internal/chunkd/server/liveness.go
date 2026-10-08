package server

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

// livenessInterval is the minimum time between sidecar list fetches for one
// org. A state file newer than the last fetch forces an early one, so a sidecar
// created mid-interval is confirmed on the next poll rather than half a minute
// later.
const livenessInterval = 30 * time.Second

// listMaxAge is how long a fetched list is trusted. Past it the list counts as
// missing, so a sidecar is no longer called confirmed on the strength of an
// answer the API has since stopped repeating (offline, revoked token). Several
// intervals long, so one slow or failed fetch does not flip every row.
const listMaxAge = 5 * time.Minute

// listFn returns the IDs of every sidecar that currently exists in orgID.
type listFn func(ctx context.Context, orgID string) (map[string]bool, error)

func listFor(client *circleci.Client) listFn {
	if client == nil {
		return nil
	}
	return func(ctx context.Context, orgID string) (map[string]bool, error) {
		sidecars, err := client.ListSidecars(ctx, orgID, false)
		if err != nil {
			return nil, err
		}
		ids := make(map[string]bool, len(sidecars))
		for _, sc := range sidecars {
			ids[sc.ID] = true
		}
		return ids, nil
	}
}

// orgList is the most recent successful sidecar list for one org.
type orgList struct {
	ids       map[string]bool
	fetchedAt time.Time
}

// livenessChecker reconciles local sidecar state against the sidecars the API
// says exist. State files outlive their sidecars — a sidecar expires, or is
// deleted from another machine — and without this check the dashboard keeps
// showing boxes that are gone until the reaper gets round to their files.
//
// It fails open, as Reap does: a sidecar is only dropped when a list fetched
// after its state file was written omits it. Without credentials, before the
// first fetch lands, or when fetches fail, sidecars are kept and marked
// unverified, since an empty or missing list is not proof of absence.
type livenessChecker struct {
	list listFn // nil when the daemon has no credentials

	mu        sync.Mutex
	lists     map[string]orgList // keyed by org ID; present only after a successful fetch
	lastFetch map[string]time.Time
	inflight  map[string]bool
	failing   map[string]bool // orgs whose last fetch failed, so a failure logs once
}

func newLivenessChecker(list listFn) *livenessChecker {
	return &livenessChecker{
		list:      list,
		lists:     make(map[string]orgList),
		lastFetch: make(map[string]time.Time),
		inflight:  make(map[string]bool),
		failing:   make(map[string]bool),
	}
}

// maybeRefresh starts a background fetch for each org that is due one. It is
// called from the poll path and must not block.
func (l *livenessChecker) maybeRefresh(ctx context.Context, sidecars []chunkd.SidecarState) {
	if l == nil || l.list == nil {
		return
	}
	newest := map[string]time.Time{}
	for _, sc := range sidecars {
		if sc.OrgID == "" {
			continue
		}
		if t, ok := newest[sc.OrgID]; !ok || sc.FileMtime.After(t) {
			newest[sc.OrgID] = sc.FileMtime
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	for org, mtime := range newest {
		last := l.lastFetch[org]
		if l.inflight[org] {
			continue
		}
		if time.Since(last) < livenessInterval && !mtime.After(last) {
			continue
		}
		l.inflight[org] = true
		go l.fetch(ctx, org)
	}
}

func (l *livenessChecker) fetch(ctx context.Context, org string) {
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Taken before the request, so a state file written while it is in flight
	// is not judged against a list that may predate its sidecar.
	started := time.Now()
	ids, err := l.list(fctx, org)

	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.inflight, org)
	// Recorded on failure too: a failing API would otherwise be retried on
	// every poll. The previous list, if any, stays in place.
	l.lastFetch[org] = started
	if err != nil {
		// Logged when it starts failing, not on every retry.
		if !l.failing[org] {
			log.Printf("chunkd: list sidecars for org %s: %v", org, err)
		}
		l.failing[org] = true
		return
	}
	delete(l.failing, org)
	l.lists[org] = orgList{ids: ids, fetchedAt: started}
}

// reconcile marks each sidecar the API has confirmed as Verified and drops the
// ones it has confirmed gone. Everything else is kept unverified. inferredOrg
// holds the IDs whose OrgID is the project's current org rather than one their
// state file recorded (see fillMissingOrg); those can be confirmed but never
// dropped. A nil checker keeps everything.
func (l *livenessChecker) reconcile(sidecars []chunkd.SidecarState, inferredOrg map[string]bool) []chunkd.SidecarState {
	if l == nil {
		return sidecars
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := sidecars[:0]
	for _, sc := range sidecars {
		list, ok := l.lists[sc.OrgID]
		if ok && time.Since(list.fetchedAt) > listMaxAge {
			ok = false
		}
		switch {
		case !ok || sc.OrgID == "":
			// Nothing to judge it against.
		case list.ids[sc.ID]:
			sc.Verified = true
		case inferredOrg[sc.ID]:
			// Possibly the wrong org's list; absence from it proves nothing.
		case list.fetchedAt.After(sc.FileMtime):
			// Gone. A list older than the state file may simply predate the
			// sidecar, so only a newer one is taken as proof.
			continue
		}
		kept = append(kept, sc)
	}
	return kept
}
