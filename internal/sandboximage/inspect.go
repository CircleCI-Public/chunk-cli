package sandboximage

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// ImageStatus says what the Docker host knows about a sandbox image's agents.
type ImageStatus string

// The values of ImageStatus.
const (
	// ImageLabeled: the image carries the LabelHarnesses label.
	ImageLabeled ImageStatus = "labeled"
	// ImageUnlabeled: the image exists but has no label, so it was built with
	// agent kit v1.
	ImageUnlabeled ImageStatus = "unlabeled"
	// ImageMissing: the image is not on the host.
	ImageMissing ImageStatus = "no_image"
)

// ImageInfo is what inspecting an image tag turns up.
type ImageInfo struct {
	Status ImageStatus
	// ID identifies the image content, to cache what running it finds.
	ID string
	// Harnesses are the agents named by the label; empty unless labeled.
	Harnesses []string
	// Kit is the kit version from LabelKit, 1 when the image has none.
	Kit int
}

// InspectImage looks tag up on the host and reads its agent labels.
func (b *Builder) InspectImage(ctx context.Context, tag string) (ImageInfo, error) {
	info, err := b.cli.ImageInspect(ctx, tag)
	switch {
	case cerrdefs.IsNotFound(err):
		return ImageInfo{Status: ImageMissing}, nil
	case err != nil:
		return ImageInfo{}, fmt.Errorf("look up image %s: %w", tag, err)
	}
	out := ImageInfo{Status: ImageUnlabeled, ID: info.ID, Kit: 1}
	if info.Config == nil {
		return out, nil
	}
	if v, err := strconv.Atoi(info.Config.Labels[LabelKit]); err == nil && v > 0 {
		out.Kit = v
	}
	if l := info.Config.Labels[LabelHarnesses]; l != "" {
		out.Status, out.Harnesses = ImageLabeled, strings.Split(l, ",")
	}
	return out, nil
}

// ProbeScript prints the agent CLIs found on the image's PATH, one per line.
const ProbeScript = "command -v claude >/dev/null && echo claude; command -v codex >/dev/null && echo codex"

// Harnesses lists the agent CLIs the image tag carries, from its
// LabelHarnesses label. An image without the label, or one that is not on this
// host, reports none.
func (b *Builder) Harnesses(ctx context.Context, tag string) ([]string, error) {
	info, err := b.InspectImage(ctx, tag)
	return info.Harnesses, err
}

// RunProbe starts a throwaway container from tag, with no network, and returns
// what ProbeScript prints. The container is removed even when ctx ends first.
func (b *Builder) RunProbe(ctx context.Context, tag string) ([]string, error) {
	created, err := b.cli.ContainerCreate(ctx, &container.Config{
		Image: tag, Cmd: []string{"sh", "-c", ProbeScript}, NetworkDisabled: true,
	}, nil, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("create probe container: %w", err)
	}
	defer func() {
		_ = b.cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
	}()
	if err := b.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("start probe container: %w", err)
	}
	waitCh, errCh := b.cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case <-waitCh:
	case err := <-errCh:
		return nil, fmt.Errorf("wait for probe container: %w", err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	logs, err := b.cli.ContainerLogs(ctx, created.ID, container.LogsOptions{ShowStdout: true})
	if err != nil {
		return nil, fmt.Errorf("read probe output: %w", err)
	}
	defer func() { _ = logs.Close() }()
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &bytes.Buffer{}, logs); err != nil {
		return nil, fmt.Errorf("read probe output: %w", err)
	}
	return strings.Fields(out.String()), nil
}
