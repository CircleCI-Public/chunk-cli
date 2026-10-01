package watchd

import (
	"context"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// ReviewPool is the sandbox pool one round draws from. It mirrors the methods of
// sidecar.Pool that RunPass needs, as fields, so a test can supply a fake
// without booting anything.
type ReviewPool struct {
	Acquire   func(context.Context) (*sidecar.PoolEntry, error)
	Release   func(*sidecar.PoolEntry)
	WaitReady func(context.Context) error
	Close     func(context.Context)
}

// ReviewPoolSpec says what pool a round needs.
type ReviewPoolSpec struct {
	// Root is the tracked project; its config decides org and image.
	Root string
	// WorkDir is the tree synced to the sandboxes: the user's project itself.
	WorkDir string
	Size    int
}

// SubmitFunc submits a script on a pool member and returns its command ID.
type SubmitFunc func(ctx context.Context, entry *sidecar.PoolEntry, script string, env map[string]string) (string, error)

// StreamFunc reads a submitted command's output to its end and returns its
// exit code.
type StreamFunc func(ctx context.Context, entry *sidecar.PoolEntry, commandID string, onOutput circleci.OutputFn) (int, error)

// ReviewConfig is everything the daemon needs to run sessions. The credential is
// resolved once by the caller at daemon start; it is only ever placed in the
// environment of a Claude command, and never logged or put in a snapshot.
type ReviewConfig struct {
	Credential review.Credential
	// BaseURL is forwarded to claude when it is not Anthropic's own.
	BaseURL string
	// AuthError explains a missing Credential, reported in the snapshot so an
	// absent capability is explained rather than silent.
	AuthError string

	// The fields below are test seams. Left nil, the daemon builds a real pool
	// from its CircleCI client and talks to the sandbox through it.
	NewPool func(ctx context.Context, spec ReviewPoolSpec) (*ReviewPool, error)
	Submit  SubmitFunc
	Stream  StreamFunc
}

// Option customizes RunDaemon.
type Option func(*daemonOptions)

type daemonOptions struct {
	review ReviewConfig
}

// WithReview configures the daemon's review capability.
func WithReview(cfg ReviewConfig) Option {
	return func(o *daemonOptions) { o.review = cfg }
}

// reviewAuthError explains why sessions cannot run, or "" when they can.
func (d *daemon) reviewAuthError() string {
	if d.rcfg.Credential.Value == "" {
		if d.rcfg.AuthError != "" {
			return d.rcfg.AuthError
		}
		return "no Claude credential configured for the watch daemon — sessions unavailable"
	}
	if d.rcfg.NewPool == nil && d.client == nil {
		return "not authenticated to CircleCI — sessions unavailable (run: chunk auth login)"
	}
	return ""
}

// lookupProject finds a tracked project by its own spelling or its
// symlink-resolved one. A root that is registered but not yet picked up by the
// poll loop is adopted, which is what lets a client register a project and start
// a session in one motion.
func (d *daemon) lookupProject(root string) *projectState {
	if root == "" {
		return nil
	}
	want := canonicalRoot(root)
	find := func() *projectState {
		d.mu.RLock()
		defer d.mu.RUnlock()
		for _, ps := range d.projects {
			if ps.root == root || ps.canonRoot == want {
				return ps
			}
		}
		return nil
	}
	if ps := find(); ps != nil {
		return ps
	}
	known, _ := sidecar.AllProjectRoots()
	for _, k := range known {
		if k != want && canonicalRoot(k) != want {
			continue
		}
		ps := d.initProject(k)
		if ps == nil {
			return nil
		}
		d.mu.Lock()
		if existing, ok := d.projects[k]; ok {
			ps = existing
		} else {
			d.projects[k] = ps
		}
		d.mu.Unlock()
		return ps
	}
	return nil
}
