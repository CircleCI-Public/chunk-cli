package docker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/system"
	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/iostream"
)

func TestEngineState(t *testing.T) {
	one := []image.Summary{{ID: "a"}}
	for _, tt := range []struct {
		name   string
		info   system.Info
		images []image.Summary
		err    error
		ready  bool
		reason string
	}{
		{"api error while starting", system.Info{}, nil, errors.New("EOF"), false, "Docker API error: EOF"},
		{"empty info", system.Info{}, nil, nil, false, "engine reports no ID or version yet"},
		{"store still loading", system.Info{ID: "i", ServerVersion: "27", Images: 3}, nil, nil, false, "engine counts 3 images but lists none"},
		{"ready with images", system.Info{ID: "i", ServerVersion: "27", Images: 1}, one, nil, true, "engine ready (1 images)"},
		{"ready with empty store", system.Info{ID: "i", ServerVersion: "27"}, nil, nil, true, "engine ready (0 images)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ready, reason := engineState(tt.info, tt.images, tt.err)
			assert.Equal(t, ready, tt.ready)
			assert.Equal(t, reason, tt.reason)
		})
	}
}

type statusLog struct{ lines []string }

func (s *statusLog) fn(_ iostream.Level, msg string) { s.lines = append(s.lines, msg) }

func TestReadyWaitsForStartingEngine(t *testing.T) {
	f := newFakeDocker()
	f.readyAt = 40 * time.Second
	var st statusLog
	assert.NilError(t, f.provider().Ready(context.Background(), "", st.fn))
	assert.Equal(t, len(f.pulls), 0)
	assert.Assert(t, len(st.lines) >= 3, "%v", st.lines)
	assert.Equal(t, st.lines[0], "Waiting for Docker to finish starting (Resource Saver)… 0s")
	assert.Equal(t, st.lines[1], "Waiting for Docker to finish starting (Resource Saver)… 6s")
	assert.Equal(t, st.lines[len(st.lines)-1], "Docker ready")
}

func TestReadyTimesOutWithoutPulling(t *testing.T) {
	f := newFakeDocker()
	f.readyAt = time.Hour
	err := f.provider().Ready(context.Background(), "alpine:3.20", (&statusLog{}).fn)
	assert.ErrorContains(t, err, "Docker did not finish starting within 3 minutes")
	assert.Equal(t, len(f.pulls), 0)
	assert.Equal(t, f.inspects, 0, "the image is not judged while the engine is starting")
}

func TestReadyMissingLocalImage(t *testing.T) {
	for _, tt := range []struct{ name, managed, image, hint string }{
		{"dockerfile.local", "", "smelt-sandbox:local", "Build it with: docker build -t smelt-sandbox:local -f Dockerfile.local ."},
		{"managed", "Dockerfile.smelt", "smelt-myrepo:3f9a", "Build it with: smelt factory image"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeDocker()
			f.hasImage = false
			p := f.provider()
			p.ManagedDockerfile = tt.managed
			err := p.Ready(context.Background(), tt.image, (&statusLog{}).fn)
			assert.ErrorContains(t, err, "Image "+tt.image+" is missing. "+tt.hint)
			assert.Equal(t, len(f.pulls), 0)
			assert.Equal(t, f.inspects, 1, "fails at once, no retry loop")
		})
	}
}

func TestReadyMissingPublicImagePulls(t *testing.T) {
	f := newFakeDocker()
	f.hasImage = false
	assert.NilError(t, f.provider().Ready(context.Background(), "alpine:3.20", (&statusLog{}).fn))
	assert.DeepEqual(t, f.pulls, []string{"alpine:3.20"})
}

func TestReadyCancelledDuringWait(t *testing.T) {
	f := newFakeDocker()
	f.readyAt = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	p := f.provider()
	p.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	err := p.Ready(ctx, "", (&statusLog{}).fn)
	assert.ErrorIs(t, err, context.Canceled)
}
