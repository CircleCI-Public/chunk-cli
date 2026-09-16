package watchd

import (
	"context"
	"fmt"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

// ProvisionRequest is sent to POST /sidecar.
type ProvisionRequest struct {
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
}

// ProvisionResponse is returned from POST /sidecar.
type ProvisionResponse struct {
	SidecarID string `json:"sidecar_id"`
}

// provisioner creates and tracks sidecars owned by the daemon.
// It is safe to call from multiple goroutines.
type provisioner struct {
	mu     sync.Mutex
	client *circleci.Client
	owned  map[string]bool // sidecar IDs created by this daemon instance
}

func newProvisioner(client *circleci.Client) *provisioner {
	return &provisioner{
		client: client,
		owned:  make(map[string]bool),
	}
}

func (p *provisioner) create(ctx context.Context, orgID, name, image string) (string, error) {
	sc, err := p.client.CreateSidecar(ctx, orgID, name, image)
	if err != nil {
		return "", fmt.Errorf("provision sidecar: %w", err)
	}
	p.mu.Lock()
	p.owned[sc.ID] = true
	p.mu.Unlock()
	return sc.ID, nil
}

func (p *provisioner) delete(ctx context.Context, id string) error {
	p.mu.Lock()
	delete(p.owned, id)
	p.mu.Unlock()
	return p.client.DeleteSidecar(ctx, id)
}

// stopAll deletes every sidecar this provisioner created. Called on daemon shutdown
// so ephemeral sidecars never outlive the daemon that owns them.
func (p *provisioner) stopAll(ctx context.Context) {
	p.mu.Lock()
	ids := make([]string, 0, len(p.owned))
	for id := range p.owned {
		ids = append(ids, id)
	}
	clear(p.owned)
	p.mu.Unlock()

	for _, id := range ids {
		_ = p.client.DeleteSidecar(ctx, id)
	}
}
