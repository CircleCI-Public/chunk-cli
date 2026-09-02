package watchd

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// credentials resolves a CircleCI client for the daemon, lazily and at most once
// per authRetryInterval after a failure.
//
// It never prompts. authprompt.ResolveCircleCIClient returns ErrNeedsAuth rather
// than asking, and prompting is the cmd layer's job — a detached background
// process has no terminal, and one blocked on a hidden prompt is
// indistinguishable from one that has hung.
//
// Resolution is deferred to first use rather than done at startup because it can
// read the OS keychain, and the daemon starts on every `chunk watch` whether or
// not anything will ever need a token.
type credentials struct {
	mu      sync.Mutex
	client  *circleci.Client
	err     error
	lastTry time.Time
}

func (c *credentials) get() (*circleci.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client != nil {
		return c.client, nil
	}
	if !c.lastTry.IsZero() && time.Since(c.lastTry) < authRetryInterval {
		return nil, c.err
	}
	c.lastTry = time.Now()

	rc, err := config.ResolveCircleCI(false)
	if err != nil {
		c.err = err
		return nil, err
	}
	client, err := authprompt.ResolveCircleCIClient(rc, nil)
	if err != nil {
		c.err = err
		return nil, err
	}
	c.client = client
	c.err = nil
	return client, nil
}

// message returns a user-facing explanation of why output is unavailable, or ""
// when credentials resolved.
func (c *credentials) message() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil || c.err == nil {
		return ""
	}
	if errors.Is(c.err, authprompt.ErrNeedsAuth) {
		return "not authenticated to CircleCI — command output unavailable (run: chunk auth login)"
	}
	return "could not authenticate to CircleCI — command output unavailable: " + c.err.Error()
}

// streamFor returns a streamFn bound to the daemon's client, or nil when no
// credentials are available. A nil streamFn means the command is still recorded
// but no output is streamed for it, which is a strictly better outcome than
// dropping the registration entirely: the dashboard can say the command ran.
func (c *credentials) streamFor() streamFn {
	client, err := c.get()
	if err != nil {
		return nil
	}
	return func(ctx context.Context, commandID string, onOutput func([]byte)) (*circleci.ExecResponse, error) {
		return client.StreamOutput(ctx, commandID, "", func(_ string, data []byte) {
			// stdout and stderr are interleaved in arrival order into one
			// buffer, which is what a terminal shows and what the developer
			// running the command locally would have seen.
			onOutput(data)
		})
	}
}
