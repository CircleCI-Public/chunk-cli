package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/client"
	"gotest.tools/v3/assert"
)

// fakeDocker stands in for the Docker daemon's image and info APIs. Embedding
// the interface satisfies the rest of client.APIClient; the provider's image
// checks only call these methods. Time is fake: the provider's sleeper
// advances it, and the daemon is "ready" once readyAt has passed.
type fakeDocker struct {
	client.APIClient
	clock    time.Time
	readyAt  time.Duration // engine answers like a starting one before this
	start    time.Time
	hasImage bool
	inspects int
	pulls    []string
	pullErr  error
	inspErr  error // returned by ImageInspect instead of not-found
}

func newFakeDocker() *fakeDocker {
	t := time.Unix(1_700_000_000, 0)
	return &fakeDocker{clock: t, start: t, hasImage: true}
}

func (f *fakeDocker) ready() bool { return f.clock.Sub(f.start) >= f.readyAt }

func (f *fakeDocker) Info(context.Context) (system.Info, error) {
	if !f.ready() {
		return system.Info{}, errors.New("starting")
	}
	return system.Info{ID: "id", ServerVersion: "27", Images: 1}, nil
}

func (f *fakeDocker) ImageList(context.Context, image.ListOptions) ([]image.Summary, error) {
	return []image.Summary{{ID: "sha256:x"}}, nil
}

func (f *fakeDocker) ImageInspect(context.Context, string, ...client.ImageInspectOption) (image.InspectResponse, error) {
	f.inspects++
	switch {
	case f.inspErr != nil:
		return image.InspectResponse{}, f.inspErr
	case f.hasImage && f.ready():
		return image.InspectResponse{}, nil
	}
	return image.InspectResponse{}, errNoSuchImage
}

func (f *fakeDocker) ImagePull(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
	f.pulls = append(f.pulls, ref)
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	return io.NopCloser(strings.NewReader("{}")), nil
}

// provider returns a Provider on f whose sleeps advance f's clock instantly.
func (f *fakeDocker) provider() *Provider {
	p := NewWithClient(f, "smelt-sandbox:local", "")
	p.now = func() time.Time { return f.clock }
	p.sleep = func(ctx context.Context, d time.Duration) error {
		f.clock = f.clock.Add(d)
		return ctx.Err()
	}
	return p
}

var errNoSuchImage = errors.Join(errors.New("no such image"), cerrdefs.ErrNotFound)

func TestEnsureImagePresentDoesNotPull(t *testing.T) {
	f := newFakeDocker()
	assert.NilError(t, f.provider().ensureImage(context.Background(), "smelt-sandbox:local"))
	assert.Equal(t, len(f.pulls), 0)
}

func TestEnsureImageMissingPublicImagePulls(t *testing.T) {
	f := newFakeDocker()
	f.hasImage = false
	assert.NilError(t, f.provider().ensureImage(context.Background(), "alpine:3.20"))
	assert.DeepEqual(t, f.pulls, []string{"alpine:3.20"})
	assert.Equal(t, f.inspects, 1, "a ready engine's not-found is trusted at once")
}

func TestEnsureImageLookupErrorIsReportedNotPulled(t *testing.T) {
	boom := errors.New("daemon unavailable")
	f := newFakeDocker()
	f.inspErr = boom
	err := f.provider().ensureImage(context.Background(), "smelt-sandbox:local")
	assert.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, "look up image smelt-sandbox:local")
	assert.Equal(t, len(f.pulls), 0)
}

func TestLocalOnly(t *testing.T) {
	for ref, want := range map[string]bool{
		"smelt-sandbox:local":          true,
		"smelt-myrepo:3f9a":            true,
		"mine:local":                   true,
		"alpine:3.20":                  false,
		"cimg/node:24.21.0":            false,
		"localhost:5000/team/app":      false,
		"localhost:5000/team/app:v1":   false,
		"registry.example/x/smelt-a:1": false,
	} {
		assert.Equal(t, localOnly(ref), want, ref)
	}
}
