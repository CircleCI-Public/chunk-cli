package docker

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/system"

	"github.com/smeltdev/smelt/internal/brand"
	"github.com/smeltdev/smelt/internal/iostream"
)

const (
	// readyTimeout bounds how long Ready waits for a starting engine.
	readyTimeout = 3 * time.Minute
	// statusInterval is how often the wait reports that it is still going.
	statusInterval = 5 * time.Second
	// maxReadyBackoff caps the delay between readiness checks.
	maxReadyBackoff = 5 * time.Second
	// localTag marks an image built on this machine, never pushed to a registry.
	localTag = "local"
)

// engineState says whether the Docker engine has finished starting, from what
// its Info and ImageList calls returned. While Docker Desktop's engine is
// starting after a Resource Saver shutdown, its API proxy answers those calls
// with an error, with an empty Info, or with no images although Info counts
// some; ImageInspect meanwhile answers "no such image" for images that exist.
// An empty store is ready only when Info agrees that it is empty.
func engineState(info system.Info, images []image.Summary, err error) (ready bool, reason string) {
	switch {
	case err != nil:
		return false, "Docker API error: " + err.Error()
	case info.ID == "" || info.ServerVersion == "":
		return false, "engine reports no ID or version yet"
	case info.Images > 0 && len(images) == 0:
		return false, fmt.Sprintf("engine counts %d images but lists none", info.Images)
	}
	return true, fmt.Sprintf("engine ready (%d images)", len(images))
}

// localOnly reports whether ref names an image smelt builds on this machine,
// which no registry has.
func localOnly(ref string) bool {
	name, tag := ref, ""
	// A colon before the last slash belongs to a registry port, not a tag.
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	return tag == localTag || strings.HasPrefix(name, brand.ImagePrefix)
}

// missingImageError is a locally built image the host lacks. It is a type so
// its message can read as a sentence for the user.
type missingImageError struct{ msg string }

func (e *missingImageError) Error() string { return e.msg }

// missingLocalImage is the error for a locally built image the host lacks.
func (b *Provider) missingLocalImage(ref string) error {
	build := "docker build -t " + ref + " -f Dockerfile.local ."
	if b.ManagedDockerfile != "" {
		build = brand.Command + " factory image"
	}
	return &missingImageError{msg: "Image " + ref + " is missing. Build it with: " + build}
}

func (b *Provider) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *Provider) pause(ctx context.Context, d time.Duration) error {
	if b.sleep != nil {
		return b.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// checkEngine asks the engine once whether it has finished starting.
func (b *Provider) checkEngine(ctx context.Context) (bool, string) {
	info, err := b.cli.Info(ctx)
	if err != nil {
		return engineState(info, nil, err)
	}
	images, err := b.cli.ImageList(ctx, image.ListOptions{})
	return engineState(info, images, err)
}

// WaitEngine blocks until the engine is ready, reporting progress, for at most
// readyTimeout. An engine that is ready at the first check costs one round trip
// and prints nothing.
func (b *Provider) WaitEngine(ctx context.Context, status iostream.StatusFunc) error {
	start := b.clock()
	var lastStatus time.Duration
	waited := false
	for attempt := 1; ; attempt++ {
		ready, reason := b.checkEngine(ctx)
		elapsed := b.clock().Sub(start)
		log.Printf("docker: readiness check %d after %s: ready=%t (%s)", attempt, elapsed.Round(time.Millisecond), ready, reason)
		if ready {
			if waited {
				status(iostream.LevelDone, "Docker ready")
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if elapsed >= readyTimeout {
			return fmt.Errorf("docker: Docker did not finish starting within %d minutes (last check: %s)", int(readyTimeout.Minutes()), reason)
		}
		if !waited || elapsed-lastStatus >= statusInterval {
			status(iostream.LevelInfo, fmt.Sprintf("Waiting for Docker to finish starting (Resource Saver)… %ds", int(elapsed.Seconds())))
			lastStatus = elapsed
		}
		waited = true
		if err := b.pause(ctx, min(time.Duration(attempt)*time.Second, maxReadyBackoff)); err != nil {
			return err
		}
	}
}

// Ready waits for the Docker engine to finish starting, then makes sure image
// is on the host (empty means the provider's default). Call it once at the
// start of a run: Create then only does a quick check, so a pool of sidecars
// does not each repeat the wait.
func (b *Provider) Ready(ctx context.Context, image string, status iostream.StatusFunc) error {
	if status == nil {
		status = func(iostream.Level, string) {}
	}
	if err := b.WaitEngine(ctx, status); err != nil {
		return err
	}
	if image == "" {
		image = b.baseImage
	}
	// A snapshot ref names a host directory; its image is checked per Create.
	if strings.HasPrefix(image, snapshotScheme) {
		return nil
	}
	return b.ensureImage(ctx, image)
}

// ensureImage makes sure ref is on the Docker host with a single lookup; it
// does not wait for a starting engine (Ready does). A missing image that smelt
// builds locally is reported with the command that builds it, since no
// registry has it; any other missing image is pulled. Other lookup errors are
// reported as themselves rather than turned into a misleading pull.
func (b *Provider) ensureImage(ctx context.Context, ref string) error {
	_, err := b.cli.ImageInspect(ctx, ref)
	switch {
	case err == nil:
		return nil
	case !cerrdefs.IsNotFound(err):
		return fmt.Errorf("docker: look up image %s: %w", ref, err)
	case localOnly(ref):
		return b.missingLocalImage(ref)
	}
	rc, err := b.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull image %s: %w", ref, err)
	}
	defer func() { _ = rc.Close() }()
	// Draining the stream is what waits for the pull to finish.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("docker: pull image %s: %w", ref, err)
	}
	return nil
}
