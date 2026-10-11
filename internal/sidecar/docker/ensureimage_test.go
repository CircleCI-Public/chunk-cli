package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"gotest.tools/v3/assert"
)

// fakeImages stands in for the Docker daemon's image API. Embedding the
// interface satisfies the rest of client.APIClient; ensureImage only calls
// these two methods.
type fakeImages struct {
	client.APIClient
	inspectErrs []error // returned by successive inspects; nil once exhausted
	inspects    int
	pulls       []string
	pullErr     error
}

func (f *fakeImages) ImageInspect(context.Context, string, ...client.ImageInspectOption) (image.InspectResponse, error) {
	f.inspects++
	if len(f.inspectErrs) == 0 {
		return image.InspectResponse{}, nil
	}
	err := f.inspectErrs[0]
	f.inspectErrs = f.inspectErrs[1:]
	return image.InspectResponse{}, err
}

func (f *fakeImages) ImagePull(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
	f.pulls = append(f.pulls, ref)
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	return io.NopCloser(strings.NewReader("{}")), nil
}

func noBackoff(t *testing.T) {
	t.Helper()
	old := inspectBackoff
	inspectBackoff = 0
	t.Cleanup(func() { inspectBackoff = old })
}

var errNoSuchImage = errors.Join(errors.New("no such image"), cerrdefs.ErrNotFound)

func TestEnsureImagePresentDoesNotPull(t *testing.T) {
	f := &fakeImages{}
	assert.NilError(t, NewWithClient(f, "", "").ensureImage(context.Background(), "smelt-sandbox:local"))
	assert.Equal(t, len(f.pulls), 0)
}

// notFoundEvery returns enough "no such image" answers to exhaust the retries.
func notFoundEvery() []error {
	errs := make([]error, notFoundAttempts)
	for i := range errs {
		errs[i] = errNoSuchImage
	}
	return errs
}

func TestEnsureImageMissingPulls(t *testing.T) {
	noBackoff(t)
	f := &fakeImages{inspectErrs: notFoundEvery()}
	assert.NilError(t, NewWithClient(f, "", "").ensureImage(context.Background(), "alpine:3.20"))
	assert.DeepEqual(t, f.pulls, []string{"alpine:3.20"})
	assert.Equal(t, f.inspects, notFoundAttempts, "a persistent not-found is checked a few times, then pulled")
}

// TestEnsureImageNotFoundWhileDockerStarts covers Docker Desktop waking from a
// Resource Saver shutdown: its API proxy answers "no such image" until the
// engine has loaded its image store. A locally built image must not be pulled.
func TestEnsureImageNotFoundWhileDockerStarts(t *testing.T) {
	noBackoff(t)
	f := &fakeImages{inspectErrs: []error{errNoSuchImage}}
	assert.NilError(t, NewWithClient(f, "", "").ensureImage(context.Background(), "smelt-sandbox:local"))
	assert.Equal(t, f.inspects, 2)
	assert.Equal(t, len(f.pulls), 0, "an image that appears on recheck must not be pulled")
}

func TestEnsureImageRetriesTransientLookupError(t *testing.T) {
	noBackoff(t)
	f := &fakeImages{inspectErrs: []error{errors.New("EOF")}}
	assert.NilError(t, NewWithClient(f, "", "").ensureImage(context.Background(), "smelt-sandbox:local"))
	assert.Equal(t, f.inspects, 2)
	assert.Equal(t, len(f.pulls), 0, "a transient lookup error must not trigger a pull")
}

func TestEnsureImagePersistentLookupErrorIsReportedNotPulled(t *testing.T) {
	noBackoff(t)
	boom := errors.New("daemon unavailable")
	f := &fakeImages{}
	for range inspectAttempts {
		f.inspectErrs = append(f.inspectErrs, boom)
	}
	err := NewWithClient(f, "", "").ensureImage(context.Background(), "smelt-sandbox:local")
	assert.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, "look up image smelt-sandbox:local")
	assert.Equal(t, f.inspects, inspectAttempts)
	assert.Equal(t, len(f.pulls), 0)
}

func TestEnsureImageMissingLocalImageExplainsBuild(t *testing.T) {
	denied := errors.New("pull access denied for smelt-sandbox")
	noBackoff(t)
	f := &fakeImages{inspectErrs: notFoundEvery(), pullErr: denied}
	err := NewWithClient(f, "", "").ensureImage(context.Background(), "smelt-sandbox:local")
	assert.ErrorIs(t, err, denied)
	assert.ErrorContains(t, err, "not on this Docker host")
	assert.ErrorContains(t, err, "docker build -t smelt-sandbox:local -f Dockerfile.local .")
}
