package docker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
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
	inspErr  error           // returned by ImageInspect instead of not-found
	listed   []image.Summary // ImageList result; nil lists one untagged image
	// byNameMissing makes inspect by name 404 while inspect by ID works.
	byNameMissing bool
	created       []string // images ContainerCreate received
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
	if f.listed != nil {
		return f.listed, nil
	}
	return []image.Summary{{ID: "sha256:x"}}, nil
}

func (f *fakeDocker) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (image.InspectResponse, error) {
	f.inspects++
	switch {
	case f.byNameMissing && strings.HasPrefix(ref, "sha256:") && f.ready():
		return image.InspectResponse{}, nil
	case f.byNameMissing:
		return image.InspectResponse{}, errNoSuchImage
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
	_, err := f.provider().resolveImage(context.Background(), "smelt-sandbox:local", nil)
	assert.NilError(t, err)
	assert.Equal(t, len(f.pulls), 0)
}

func TestEnsureImageMissingPublicImagePulls(t *testing.T) {
	f := newFakeDocker()
	f.hasImage = false
	_, err := f.provider().resolveImage(context.Background(), "alpine:3.20", nil)
	assert.NilError(t, err)
	assert.DeepEqual(t, f.pulls, []string{"alpine:3.20"})
	assert.Equal(t, f.inspects, 1, "a ready engine's not-found is trusted at once")
}

func TestEnsureImageLookupErrorIsReportedNotPulled(t *testing.T) {
	boom := errors.New("daemon unavailable")
	f := newFakeDocker()
	f.inspErr = boom
	_, err := f.provider().resolveImage(context.Background(), "smelt-sandbox:local", nil)
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

const observedID = "sha256:43b3eb388951aaaa"

// observedState replays what Docker Desktop reported: the image is listed,
// inspect by name is 404 every time, inspect by ID works.
func observedState() *fakeDocker {
	f := newFakeDocker()
	f.byNameMissing = true
	f.listed = []image.Summary{{ID: observedID, RepoTags: []string{"smelt-sandbox:local"}}}
	return f
}

func TestDecideImage(t *testing.T) {
	listed := func(tags ...string) []image.Summary { return []image.Summary{{ID: "sha256:abc", RepoTags: tags}} }
	for _, tt := range []struct {
		name   string
		ref    string
		ready  bool
		images []image.Summary
		want   imageDecision
		id     string
	}{
		{"listed by name", "smelt-sandbox:local", true, listed("smelt-sandbox:local"), imageByID, "sha256:abc"},
		{"listed with docker.io prefix", "alpine:3.20", true, listed("docker.io/library/alpine:3.20"), imageByID, "sha256:abc"},
		{"ref without tag is latest", "alpine", true, listed("alpine:latest"), imageByID, "sha256:abc"},
		{"listed without tag in ref but other tag", "alpine", true, listed("alpine:3.20"), imageMissing, ""},
		{"not listed, ready", "smelt-sandbox:local", true, listed("other:local"), imageMissing, ""},
		{"engine not ready", "smelt-sandbox:local", false, listed("smelt-sandbox:local"), imageNotReady, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, id := decideImage(tt.ref, tt.ready, tt.images)
			assert.Equal(t, got, tt.want)
			assert.Equal(t, id, tt.id)
		})
	}
}

func TestResolveImageListedButNotInspectableByNameUsesID(t *testing.T) {
	f := observedState()
	start := f.clock
	var st statusLog
	got, err := f.provider().resolveImage(context.Background(), "smelt-sandbox:local", st.fn)
	assert.NilError(t, err)
	assert.Equal(t, got, observedID)
	assert.Equal(t, len(f.pulls), 0)
	assert.Equal(t, f.clock, start, "no wait")
	assert.Equal(t, len(st.lines), 0)
}

func TestReadyListedButNotInspectableByNameSucceedsAtOnce(t *testing.T) {
	f := observedState()
	start := f.clock
	assert.NilError(t, f.provider().Ready(context.Background(), "", (&statusLog{}).fn))
	assert.Equal(t, f.clock, start, "no wait")
	assert.Equal(t, len(f.pulls), 0)
}

func TestResolveImageWaitsForStartingEngineThenUsesID(t *testing.T) {
	f := observedState()
	f.readyAt = 20 * time.Second
	got, err := f.provider().resolveImage(context.Background(), "smelt-sandbox:local", nil)
	assert.NilError(t, err)
	assert.Equal(t, got, observedID)
}

func TestResolveImageNotListedIsMissingImmediately(t *testing.T) {
	f := observedState()
	f.listed = []image.Summary{{ID: "sha256:y", RepoTags: []string{"other:local"}}}
	start := f.clock
	_, err := f.provider().resolveImage(context.Background(), "smelt-sandbox:local", nil)
	assert.ErrorContains(t, err, "Image smelt-sandbox:local is missing")
	assert.Equal(t, f.clock, start, "no wait")
}

func TestCreateAndSnapshotUseResolvedID(t *testing.T) {
	f := observedState()
	p := f.provider()
	p.workRoot = t.TempDir()
	ctx := context.Background()
	inst, err := p.Create(ctx, "seed", "", "/repo")
	assert.NilError(t, err)
	assert.DeepEqual(t, f.created, []string{observedID})

	ref, err := p.Snapshot(ctx, inst.ID, "snap")
	assert.NilError(t, err)
	recorded, err := os.ReadFile(filepath.Join(strings.TrimPrefix(ref, snapshotScheme), snapshotImageFile))
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(string(recorded)), observedID)

	_, err = p.Create(ctx, "clone", ref, "/repo")
	assert.NilError(t, err)
	assert.DeepEqual(t, f.created, []string{observedID, observedID})
}

func (f *fakeDocker) ContainerCreate(_ context.Context, cfg *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
	f.created = append(f.created, cfg.Image)
	return container.CreateResponse{ID: "c" + strconv.Itoa(len(f.created))}, nil
}

func (f *fakeDocker) ContainerStart(context.Context, string, container.StartOptions) error {
	return nil
}
