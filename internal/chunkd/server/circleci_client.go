package server

import (
	"context"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// ClientResolver resolves the CircleCI client on demand, returning the message
// that explains a failure to resolve one. It runs at most once.
type ClientResolver func() (*circleci.Client, string)

// WithClientResolver makes the daemon resolve its CircleCI client the first
// time a request needs one, instead of at startup. Without it, the daemon only
// has a client if RunDaemon was handed one.
func WithClientResolver(resolve ClientResolver) Option {
	return func(o *daemonOptions) { o.resolve = resolve }
}

// cciClient is the daemon's CircleCI client, resolved lazily and once.
type cciClient struct {
	resolve ClientResolver

	mu       sync.Mutex
	resolved bool
	client   *circleci.Client
	// authError explains an absent client to the user, or "" when there is
	// nothing to explain.
	authError string
}

func (c *cciClient) authMessage() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authError
}

// circleCIClient returns the CircleCI client, resolving it on first use, or nil
// when none could be. Only code that is about to talk to CircleCI on behalf of
// a project that chose CircleCI may call it.
func (d *daemon) circleCIClient() *circleci.Client {
	d.cci.mu.Lock()
	if d.cci.resolved || d.cci.resolve == nil {
		client := d.cci.client
		d.cci.mu.Unlock()
		return client
	}
	d.cci.mu.Unlock()

	// Resolved outside the lock: reading the keychain can be slow.
	client, msg := d.cci.resolve()
	d.adoptClient(client, msg)
	return d.cci.get()
}

func (c *cciClient) get() *circleci.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client
}

// adoptClient installs client, once, and wires it into the parts of the daemon
// that talk to CircleCI.
func (d *daemon) adoptClient(client *circleci.Client, msg string) {
	d.cci.mu.Lock()
	if d.cci.resolved {
		d.cci.mu.Unlock()
		return
	}
	d.cci.resolved = true
	d.cci.client = client
	d.cci.authError = msg
	d.cci.mu.Unlock()

	if client == nil {
		return
	}
	d.mu.Lock()
	d.prov = newProvisioner(client)
	d.mu.Unlock()
	d.res.setClient(client)
	d.live.setList(listFor(client))
}

// provisioner returns the sidecar provisioner, resolving the CircleCI client
// first. Nil when there is no client.
func (d *daemon) provisioner() *provisioner {
	d.circleCIClient()
	return d.currentProvisioner()
}

// currentProvisioner returns the provisioner if one exists, without resolving
// a client to make one.
func (d *daemon) currentProvisioner() *provisioner {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.prov
}

func (d *daemon) stopProvisioner() {
	if prov := d.currentProvisioner(); prov != nil {
		prov.stopAll(context.Background())
	}
}

// factoryClient is the CircleCI client a factory run on backend needs: none for
// Docker, which must not construct one.
func (d *daemon) factoryClient(backend string) *circleci.Client {
	if config.BackendOrDefault(backend) == chunkd.BackendDocker {
		return nil
	}
	return d.circleCIClient()
}

// projectUsesCircleCI reports whether the project at dir opted into the
// CircleCI backend. A project whose config cannot be read has not opted in.
func projectUsesCircleCI(dir string) bool {
	if dir == "" {
		return false
	}
	cfg, err := config.LoadProjectConfig(dir)
	return err == nil && cfg.UsesCircleCI()
}
