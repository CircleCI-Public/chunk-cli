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

// checkEngine asks the engine once whether it has finished starting, and
// returns the images it listed.
func (b *Provider) checkEngine(ctx context.Context) (ready bool, reason string, images []image.Summary) {
	info, err := b.cli.Info(ctx)
	if err != nil {
		ready, reason = engineState(info, nil, err)
		return ready, reason, nil
	}
	images, err = b.cli.ImageList(ctx, image.ListOptions{})
	ready, reason = engineState(info, images, err)
	return ready, reason, images
}

// WaitEngine blocks until the engine is ready, reporting progress, for at most
// readyTimeout. An engine that is ready at the first check costs one round trip
// and prints nothing.
func (b *Provider) WaitEngine(ctx context.Context, status iostream.StatusFunc) error {
	start := b.clock()
	var lastStatus time.Duration
	waited := false
	for attempt := 1; ; attempt++ {
		ready, reason, _ := b.checkEngine(ctx)
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
	_, err := b.resolveImage(ctx, image, status)
	return err
}

// canonicalRef normalises an image reference for comparison: the implicit
// docker.io/library/ prefix is dropped and a missing tag becomes :latest.
func canonicalRef(ref string) string {
	ref = strings.TrimPrefix(ref, "docker.io/library/")
	ref = strings.TrimPrefix(ref, "library/")
	if i := strings.LastIndex(ref, ":"); i <= strings.LastIndex(ref, "/") && !strings.Contains(ref, "@") {
		ref += ":latest"
	}
	return ref
}

// listedImageID finds ref among the listed images' tags and returns its ID.
func listedImageID(ref string, images []image.Summary) (string, bool) {
	want := canonicalRef(ref)
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if canonicalRef(tag) == want {
				return img.ID, true
			}
		}
	}
	return "", false
}

// imageDecision is what a presence check concluded.
type imageDecision int

const (
	imagePresent  imageDecision = iota // inspect found it by name
	imageByID                          // listed, but only inspectable by ID
	imageMissing                       // engine ready and the image not listed
	imageNotReady                      // engine not ready; wait and ask again
)

func (d imageDecision) String() string {
	return [...]string{"present", "present-by-id", "missing", "wait"}[d]
}

// decideImage is the presence decision after ImageInspect(ref) was not found.
// Docker Desktop's containerd image store can answer 404 by name for a listed
// image indefinitely, so the list is the source of truth and the listed ID,
// which inspects and runs reliably, is what containers are created from.
func decideImage(ref string, engineReady bool, images []image.Summary) (imageDecision, string) {
	if !engineReady {
		return imageNotReady, ""
	}
	if id, ok := listedImageID(ref, images); ok {
		return imageByID, id
	}
	return imageMissing, ""
}

func shortID(id string) string {
	return strings.TrimPrefix(id, "sha256:")[:min(12, len(strings.TrimPrefix(id, "sha256:")))]
}

// resolveImage makes sure ref is on the Docker host and returns the reference
// to create containers from: ref itself, or the image's ID when Docker lists
// the image but cannot inspect it by name. A missing image that smelt builds
// locally is reported with the command that builds it, since no registry has
// it; any other missing image is pulled. A starting engine is waited for as
// WaitEngine does. Other lookup errors are reported as themselves rather than
// turned into a misleading pull.
func (b *Provider) resolveImage(ctx context.Context, ref string, status iostream.StatusFunc) (string, error) {
	if status == nil {
		status = func(iostream.Level, string) {}
	}
	start := b.clock()
	_, err := b.cli.ImageInspect(ctx, ref)
	switch {
	case err == nil:
		log.Printf("docker image check: ref=%s inspect=200 decision=%s elapsed=%s", ref, imagePresent, b.clock().Sub(start).Round(time.Millisecond))
		return ref, nil
	case !cerrdefs.IsNotFound(err):
		return "", fmt.Errorf("docker: look up image %s: %w", ref, err)
	}
	for {
		ready, reason, images := b.checkEngine(ctx)
		decision, id := decideImage(ref, ready, images)
		_, listed := listedImageID(ref, images)
		log.Printf("docker image check: ref=%s inspect=404 listed=%t id=%s decision=%s elapsed=%s (%s)",
			ref, listed, shortID(id), decision, b.clock().Sub(start).Round(time.Millisecond), reason)
		if decision == imageNotReady {
			if err := b.WaitEngine(ctx, status); err != nil {
				return "", err
			}
			continue
		}
		if decision == imageByID {
			log.Printf("docker: %s is listed but not inspectable by name; using its ID %s", ref, shortID(id))
			return id, nil
		}
		break
	}
	if localOnly(ref) {
		return "", b.missingLocalImage(ref)
	}
	rc, err := b.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return "", fmt.Errorf("docker: pull image %s: %w", ref, err)
	}
	defer func() { _ = rc.Close() }()
	// Draining the stream is what waits for the pull to finish.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return "", fmt.Errorf("docker: pull image %s: %w", ref, err)
	}
	return ref, nil
}
